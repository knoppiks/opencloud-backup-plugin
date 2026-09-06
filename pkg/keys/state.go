package keys

// The durable keys.Store, on top of pkg/state.
//
// What is persisted is exactly what the in-memory store holds: wrapped
// envelopes. The SRW envelope is ciphertext only openable with the cluster/KMS
// SRW key, and the RK envelope only with the user's Recovery Key, which the
// server never holds. Persisting them therefore does not widen what a stolen
// state store yields — it is the same class of material the S3 target already
// carries (decisions.md, Phase-5 amendment) — and it is what lets an unattended
// run work after a restart at all.
//
// Store's methods predate this implementation and carry no context (they are
// called from request handlers and the worker alike), so each operation runs on
// its own bounded context rather than an unbounded background one.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"opencloud-backup-plugin/pkg/state"
)

// keysPrefix roots per-Space key records.
const keysPrefix = "keys"

// storeTimeout bounds a single state operation.
const storeTimeout = 30 * time.Second

// StateStore is a Store backed by durable state.
type StateStore struct {
	docs  *state.Documents[SpaceKeys]
	clock Clock
}

var _ Store = (*StateStore)(nil)

// NewStateStore returns a Store persisting to st.
func NewStateStore(st state.Store, clock Clock) *StateStore {
	if clock == nil {
		clock = systemClock{}
	}
	return &StateStore{docs: state.NewDocuments[SpaceKeys](st, keysPrefix), clock: clock}
}

// PutSRW stores the SRW-wrapped DK for a space.
func (s *StateStore) PutSRW(spaceID string, w WrappedDK) error {
	if w.Kind != WrapSRW {
		return ErrBadEnvelope
	}
	return s.update(spaceID, func(rec *SpaceKeys) { rec.SRW = cloneWrapped(w) })
}

// GetSRW returns the SRW-wrapped DK for a space.
func (s *StateStore) GetSRW(spaceID string) (WrappedDK, error) {
	rec, err := s.read(spaceID)
	if err != nil {
		return WrappedDK{}, err
	}
	if rec.SRW.Kind != WrapSRW {
		return WrappedDK{}, ErrNotFound{SpaceID: spaceID}
	}
	return cloneWrapped(rec.SRW), nil
}

// PutRK stores the RK-wrapped DK for a space.
func (s *StateStore) PutRK(spaceID string, w WrappedDK) error {
	if w.Kind != WrapRK {
		return ErrBadEnvelope
	}
	return s.update(spaceID, func(rec *SpaceKeys) { rec.RK = cloneWrapped(w) })
}

// GetRK returns the RK-wrapped DK for a space.
func (s *StateStore) GetRK(spaceID string) (WrappedDK, error) {
	rec, err := s.read(spaceID)
	if err != nil {
		return WrappedDK{}, err
	}
	if rec.RK.Kind != WrapRK {
		return WrappedDK{}, ErrNotFound{SpaceID: spaceID}
	}
	return cloneWrapped(rec.RK), nil
}

// Status reports setup state for a space without revealing key material.
func (s *StateStore) Status(spaceID string) (Status, error) {
	rec, err := s.read(spaceID)
	if err != nil {
		var notFound ErrNotFound
		if errors.As(err, &notFound) {
			// "No keys yet" is a normal answer for a Space that has not been
			// set up, not an error.
			return Status{SpaceID: spaceID}, nil
		}
		return Status{}, err
	}
	return statusOf(rec), nil
}

// read loads a Space's key record.
func (s *StateStore) read(spaceID string) (SpaceKeys, error) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()

	rec, err := s.docs.Get(ctx, spaceID)
	if err != nil {
		if state.IsNotFound(err) {
			return SpaceKeys{}, ErrNotFound{SpaceID: spaceID}
		}
		// Never surface the underlying detail: it describes stored envelopes.
		return SpaceKeys{}, errors.New("keys: could not read the key record")
	}
	return rec, nil
}

// update applies mutate to a Space's record, creating it when absent.
func (s *StateStore) update(spaceID string, mutate func(*SpaceKeys)) error {
	if spaceID == "" {
		return fmt.Errorf("keys: space id required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()

	now := s.clock.Now().UTC()
	rec, err := s.docs.Get(ctx, spaceID)
	switch {
	case err == nil:
	case state.IsNotFound(err):
		rec = SpaceKeys{SpaceID: spaceID, CreatedAt: now}
	default:
		return errors.New("keys: could not read the key record")
	}

	mutate(&rec)
	rec.SpaceID = spaceID
	rec.UpdatedAt = now
	if err := s.docs.Put(ctx, rec, spaceID); err != nil {
		return errors.New("keys: could not store the key record")
	}
	return nil
}

// statusOf derives the key-material-free summary of a record. Shared with the
// in-memory store so both report identically.
func statusOf(rec SpaceKeys) Status {
	hasRK := rec.RK.Kind == WrapRK
	hasSRW := rec.SRW.Kind == WrapSRW
	return Status{
		SpaceID:    rec.SpaceID,
		Configured: hasRK && hasSRW,
		HasRK:      hasRK,
		HasSRW:     hasSRW,
		RKVersion:  rec.RK.Version,
		SRWVersion: rec.SRW.Version,
		CreatedAt:  rec.CreatedAt,
		UpdatedAt:  rec.UpdatedAt,
	}
}
