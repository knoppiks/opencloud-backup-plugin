package takeout

// Publishing the Space's key envelope to the target (Path A precondition).
//
// A Take-Out must be usable with OpenCloud fully down, so the RK-wrapped Data
// Key envelope cannot live only in the service's key store: the worker copies it
// to the target next to the repository. What is published is *ciphertext* — the
// Recovery Key is neither held nor learned by the server (decisions.md, trust &
// key model), so this does not widen what the buddy store can read.
//
// The envelope is written outside the kopia repository prefix
// (snapshot.EnvelopeKey), because everything under a repo prefix is kopia-owned
// and maintenance may reclaim blobs it does not recognise.

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

// Publisher publishes a Space's RK-wrapped Data Key envelope to a target.
type Publisher interface {
	// Publish stores blob as the Space's recovery envelope. It is idempotent:
	// republishing identical bytes is a no-op.
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

	// Refuse to publish anything that is not an RK envelope: this object is the
	// user's only recovery path, and a wrong blob here is silent data loss.
	info, err := keys.Inspect(blob)
	if err != nil || info.Kind != keys.WrapRK {
		return fmt.Errorf("takeout: refusing to publish a non-recovery envelope")
	}

	key := snapshot.EnvelopeKey(snapshot.Location{Prefix: prefix}, snapshot.SpaceRef{SpaceID: spaceID})

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
