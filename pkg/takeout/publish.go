package takeout

// Publishing the Space's key envelopes to the target.
//
// Two envelopes, two reasons:
//
//   - The RK-wrapped one (snapshot.EnvelopeKey) is a Path A precondition: a
//     Take-Out must be usable with OpenCloud fully down, so it cannot live only
//     in the service's key store. It is ciphertext the server cannot open — the
//     Recovery Key is neither held nor learned by it (decisions.md, trust & key
//     model) — so publishing it does not widen what the buddy store can read.
//
//   - The SRW-wrapped one (snapshot.ServerEnvelopeKey) is the service's own
//     copy, so that the state Space is not the only place it exists. Losing the
//     state Space then costs a re-configuration rather than every unattended
//     backup for that Space. This one *is* a trust-model trade-off: it is
//     openable by whoever holds the cluster's SRW key, and it is recorded as
//     such in decisions.md.
//
// Both are written outside the kopia repository prefix, because everything under
// a repo prefix is kopia-owned and maintenance may reclaim blobs it does not
// recognise.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/objstore"
	"opencloud-backup-plugin/pkg/snapshot"
)

// PublishTarget addresses the target a Space's envelope is published to. Its
// credentials are plaintext and therefore exist only in worker memory for the
// duration of a run (decisions.md #14); they are never logged.
type PublishTarget struct {
	// S3 is the connection configuration, including the bucket.
	S3 objstore.S3Config
	// Prefix is the deployment prefix inside the bucket.
	Prefix string
}

// Publisher publishes a Space's wrapped Data Key envelope to a target. The
// object it is stored under follows from the envelope's kind, so a caller
// cannot file a server envelope where the recovery one belongs.
type Publisher interface {
	// Publish stores blob as the Space's recovery (RK) or server (SRW)
	// envelope. It is idempotent: republishing identical bytes is a no-op.
	Publish(ctx context.Context, target PublishTarget, spaceID string, blob []byte) error
}

// S3Publisher is the production Publisher.
type S3Publisher struct{}

var _ Publisher = S3Publisher{}

// Publish writes the envelope to the target, skipping the write when the stored
// envelope is already identical.
func (S3Publisher) Publish(ctx context.Context, target PublishTarget, spaceID string, blob []byte) error {
	store, err := objstore.NewS3(ctx, target.S3)
	if err != nil {
		return err
	}
	return PublishTo(ctx, store, target.Prefix, spaceID, blob)
}

// PublishTo writes an envelope to an already-open store. It is the testable core
// of Publish and is used directly by tests and local tooling.
func PublishTo(ctx context.Context, store objstore.Store, prefix, spaceID string, blob []byte) error {
	if store == nil {
		return fmt.Errorf("takeout: object store is required")
	}
	if spaceID == "" {
		return fmt.Errorf("takeout: space id is required")
	}

	// The object name follows from the envelope's own header, not from the
	// caller: the recovery object is the user's only recovery path, and a wrong
	// blob there is silent data loss. Anything that is not a Data Key envelope
	// (a TW-wrapped credential blob, a truncated file) is refused outright.
	info, err := keys.Inspect(blob)
	if err != nil {
		return fmt.Errorf("takeout: refusing to publish an unreadable envelope")
	}

	loc := snapshot.Location{Prefix: prefix}
	ref := snapshot.SpaceRef{SpaceID: spaceID}

	var key string
	switch info.Kind {
	case keys.WrapRK:
		key = snapshot.EnvelopeKey(loc, ref)
	case keys.WrapSRW:
		key = snapshot.ServerEnvelopeKey(loc, ref)
	default:
		return fmt.Errorf("takeout: refusing to publish a %s envelope", info.Kind)
	}

	if same, err := storedEnvelopeMatches(ctx, store, key, blob); err != nil {
		return err
	} else if same {
		return nil
	}
	return store.Put(ctx, key, blob)
}

// storedEnvelopeMatches reports whether the target already holds these exact
// bytes, so an unchanged envelope is not rewritten on every run.
func storedEnvelopeMatches(ctx context.Context, store objstore.Store, key string, blob []byte) (bool, error) {
	rc, err := store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, objstore.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = rc.Close() }()

	existing, err := io.ReadAll(rc)
	if err != nil {
		return false, fmt.Errorf("takeout: read stored key envelope: %w", err)
	}
	return bytes.Equal(existing, blob), nil
}
