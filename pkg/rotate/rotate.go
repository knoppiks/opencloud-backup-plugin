// Package rotate re-wraps stored ciphertext under a new custody key.
//
// Two keys live in the cluster and can leak there: the SRW key, which wraps
// every Space's Data Key, and the TW key, which wraps every target's S3
// credentials (decisions.md #14). Until now neither could be replaced. A
// suspected exposure therefore had no remedy short of re-running the key
// ceremony for every Space — the operation that destroys backup history — or
// re-entering every target credential by hand.
//
// What rotation does and does not touch is the whole point:
//
//   - The Data Key is unchanged. Envelopes are re-wrapped, not re-keyed, so no
//     snapshot is rewritten, no data is re-uploaded, and every existing backup
//     stays readable.
//   - The user's Recovery Key is unchanged and unaffected. It is not in this
//     package: the plaintext RK never reaches the server, so RK rotation happens
//     in the browser and arrives as a finished envelope (see pkg/api).
//   - Nothing here logs, returns, or formats key material. Errors name the
//     record that failed, never its contents.
//
// Rotation is resumable rather than transactional. The backend has no
// transactions (pkg/state), so a rotation interrupted halfway leaves some
// records on the new key and some on the old. Re-running finishes the job:
// a record that no longer opens with the old key but does open with the new one
// is counted as already rotated and left alone. A record that opens with
// neither is a hard error — that is corruption or the wrong key pair, and
// carrying on would rewrite the rest under an assumption that has already
// failed once.
package rotate

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"

	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/targets"
)

// Result counts what a rotation did. AlreadyRotated is not a warning: it is the
// normal outcome of finishing an interrupted run.
type Result struct {
	// Rotated is how many records were re-wrapped under the new key.
	Rotated int
	// AlreadyRotated is how many were found to be on the new key already.
	AlreadyRotated int
	// Skipped is how many records held nothing to rotate.
	Skipped int
}

// ErrKeyMismatch is returned when a stored record opens under neither the old
// nor the new key. It means the wrong old key was supplied, or the record is
// corrupt; either way the rotation stops rather than continuing on a false
// assumption.
var ErrKeyMismatch = errors.New("rotate: record opens with neither the old nor the new key")

// EnvelopeStore is the part of keys.Store a server-key rotation needs.
type EnvelopeStore interface {
	// Spaces returns every space holding an envelope.
	Spaces() ([]string, error)
	// GetSRW returns a space's server envelope.
	GetSRW(spaceID string) (keys.WrappedDK, error)
	// PutSRW stores a space's server envelope.
	PutSRW(spaceID string, w keys.WrappedDK) error
}

// SRW re-wraps every Space's Data Key from oldKey to newKey.
//
// A Space with no server envelope is skipped, not failed: a half-finished key
// ceremony must not block the rotation of every other Space.
func SRW(store EnvelopeStore, oldKey, newKey []byte) (Result, error) {
	if err := checkKeyPair(oldKey, newKey, keys.SRWKeySize); err != nil {
		return Result{}, err
	}

	spaces, err := store.Spaces()
	if err != nil {
		return Result{}, fmt.Errorf("rotate: list spaces: %w", err)
	}

	var out Result
	for _, spaceID := range spaces {
		current, err := store.GetSRW(spaceID)
		if err != nil {
			var notFound keys.ErrNotFound
			if errors.As(err, &notFound) {
				out.Skipped++
				continue
			}
			return out, fmt.Errorf("rotate: read the server envelope of space %s: %w", spaceID, err)
		}

		rotated, err := keys.RotateSRW(current, oldKey, newKey)
		if err != nil {
			// Either this envelope is already on the new key (a re-run finishing
			// an interrupted rotation) or something is wrong that rewriting will
			// not fix.
			if alreadyRotated(current, newKey) {
				out.AlreadyRotated++
				continue
			}
			return out, fmt.Errorf("%w: space %s", ErrKeyMismatch, spaceID)
		}
		if err := store.PutSRW(spaceID, rotated); err != nil {
			return out, fmt.Errorf("rotate: store the server envelope of space %s: %w", spaceID, err)
		}
		out.Rotated++
	}
	return out, nil
}

// alreadyRotated reports whether an envelope already opens under the new key.
func alreadyRotated(w keys.WrappedDK, newKey []byte) bool {
	dk, err := keys.UnwrapSRW(w, newKey)
	if err != nil {
		return false
	}
	keys.Zeroize(dk)
	return true
}

// TargetStore is the part of targets.Store a credential rotation needs.
type TargetStore interface {
	// ListTargets returns every target, including its sealed credentials.
	ListTargets(ctx context.Context) ([]targets.Target, error)
	// UpdateTarget stores a target; a non-nil WrappedCreds replaces the sealed
	// credentials.
	UpdateTarget(ctx context.Context, t targets.Target) (targets.Target, error)
}

// TW re-seals every target's S3 credentials from oldKey to newKey.
//
// The plaintext credentials exist in memory for the duration of one re-seal,
// which is the same exposure a backup run already has (decisions.md #14). They
// are never logged and never leave this function.
func TW(ctx context.Context, store TargetStore, oldKey, newKey []byte) (Result, error) {
	if err := checkKeyPair(oldKey, newKey, keys.SRWKeySize); err != nil {
		return Result{}, err
	}
	oldSealer, err := targets.NewCredSealer(oldKey)
	if err != nil {
		return Result{}, err
	}
	newSealer, err := targets.NewCredSealer(newKey)
	if err != nil {
		return Result{}, err
	}

	all, err := store.ListTargets(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("rotate: list targets: %w", err)
	}

	var out Result
	for _, t := range all {
		if len(t.WrappedCreds) == 0 {
			out.Skipped++
			continue
		}

		creds, err := oldSealer.Open(t.WrappedCreds)
		if err != nil {
			if _, err := newSealer.Open(t.WrappedCreds); err == nil {
				out.AlreadyRotated++
				continue
			}
			return out, fmt.Errorf("%w: target %s", ErrKeyMismatch, t.ID)
		}

		sealed, version, err := newSealer.Seal(creds)
		if err != nil {
			return out, fmt.Errorf("rotate: seal the credentials of target %s: %w", t.ID, err)
		}
		t.WrappedCreds, t.Version = sealed, version
		if _, err := store.UpdateTarget(ctx, t); err != nil {
			return out, fmt.Errorf("rotate: store target %s: %w", t.ID, err)
		}
		out.Rotated++
	}
	return out, nil
}

// checkKeyPair rejects the argument mistakes that would otherwise be discovered
// halfway through a rotation, when half the records have already moved.
func checkKeyPair(oldKey, newKey []byte, size int) error {
	switch {
	case len(oldKey) != size:
		return fmt.Errorf("rotate: the old key must be %d bytes", size)
	case len(newKey) != size:
		return fmt.Errorf("rotate: the new key must be %d bytes", size)
	case subtle.ConstantTimeCompare(oldKey, newKey) == 1:
		return errors.New("rotate: the old and new keys are identical; there is nothing to rotate")
	}
	return nil
}
