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
// Layout (append-only, decisions.md #16):
//
//	keyenvelopes/<space-id>/rk/<nanos>
//	keyenvelopes/<space-id>/srw/<nanos>
//
// One envelope per document, and a write never touches an existing one. This is
// the record whose loss cannot be repaired: without an envelope, every snapshot
// already written for that Space is unreadable forever. Replacing it in place on
// a backend with no transactions means a crash mid-write destroys it, so the
// service does not do that — it appends and reads the newest. Superseded
// versions stay: they are small ciphertext blobs and the only audit trail a key
// rotation leaves.
//
// Records written before versioning existed live at "keys/<space-id>" as a
// single document holding both envelopes. They are still read when a Space has
// no versioned envelope yet, and they are never rewritten or deleted.
//
// Store's methods predate this implementation and carry no context (they are
// called from request handlers and the worker alike), so each operation runs on
// its own bounded context rather than an unbounded background one.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"opencloud-backup-plugin/pkg/state"
)

const (
	// envelopePrefix roots the append-only envelope documents.
	envelopePrefix = "keyenvelopes"
	// legacyKeysPrefix is the pre-versioned layout: one record per Space.
	legacyKeysPrefix = "keys"
	// kindRK / kindSRW are the per-kind key segments under a Space.
	kindRK  = "rk"
	kindSRW = "srw"
)

// storeTimeout bounds a single state operation.
const storeTimeout = 30 * time.Second

// StateStore is a Store backed by durable state.
type StateStore struct {
	envelopes *state.Versions[WrappedDK]
	legacy    *state.Documents[SpaceKeys]
	clock     Clock
}

var _ Store = (*StateStore)(nil)

// NewStateStore returns a Store persisting to st.
func NewStateStore(st state.Store, clock Clock) *StateStore {
	if clock == nil {
		clock = systemClock{}
	}
	return &StateStore{
		envelopes: state.NewVersions[WrappedDK](st, envelopePrefix),
		legacy:    state.NewDocuments[SpaceKeys](st, legacyKeysPrefix),
		clock:     clock,
	}
}

// PutSRW stores the SRW-wrapped DK for a space.
func (s *StateStore) PutSRW(spaceID string, w WrappedDK) error {
	if w.Kind != WrapSRW {
		return ErrBadEnvelope
	}
	return s.append(spaceID, kindSRW, w)
}

// GetSRW returns the SRW-wrapped DK for a space.
func (s *StateStore) GetSRW(spaceID string) (WrappedDK, error) {
	return s.newest(spaceID, kindSRW, WrapSRW)
}

// PutRK stores the RK-wrapped DK for a space.
func (s *StateStore) PutRK(spaceID string, w WrappedDK) error {
	if w.Kind != WrapRK {
		return ErrBadEnvelope
	}
	return s.append(spaceID, kindRK, w)
}

// GetRK returns the RK-wrapped DK for a space.
func (s *StateStore) GetRK(spaceID string) (WrappedDK, error) {
	return s.newest(spaceID, kindRK, WrapRK)
}

// Status reports setup state for a space without revealing key material.
func (s *StateStore) Status(spaceID string) (Status, error) {
	ctx, cancel := s.context()
	defer cancel()

	rec, err := s.read(ctx, spaceID)
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

// Spaces returns the ids of every space holding an envelope, in order. It reads
// no documents: the ids are in the keys.
func (s *StateStore) Spaces() ([]string, error) {
	ctx, cancel := s.context()
	defer cancel()

	// The versioned and pre-versioned layouts are separate collections here
	// (they hold different document types), so both are asked and the union is
	// reported: a Space set up before versioning must not be skipped by a
	// rotation.
	versioned, err := s.envelopes.IDs(ctx)
	if err != nil {
		// Never surface the underlying detail: it describes stored envelopes.
		return nil, errors.New("keys: could not list the key records")
	}
	legacy, err := s.legacy.IDs(ctx)
	if err != nil {
		return nil, errors.New("keys: could not list the key records")
	}
	return mergeIDs(versioned, legacy), nil
}

// newest returns a Space's current envelope of one kind.
func (s *StateStore) newest(spaceID, kind string, want WrapKind) (WrappedDK, error) {
	ctx, cancel := s.context()
	defer cancel()

	w, err := s.envelope(ctx, spaceID, kind)
	if err != nil {
		return WrappedDK{}, err
	}
	if w.Kind != want {
		return WrappedDK{}, ErrNotFound{SpaceID: spaceID}
	}
	return cloneWrapped(w), nil
}

// envelope reads one kind of envelope, falling back to the pre-versioned record.
func (s *StateStore) envelope(ctx context.Context, spaceID, kind string) (WrappedDK, error) {
	w, _, err := s.envelopes.Load(ctx, spaceID, kind)
	switch {
	case err == nil:
		return w, nil
	case state.IsNotFound(err):
	default:
		// Never surface the underlying detail: it describes stored envelopes.
		return WrappedDK{}, errors.New("keys: could not read the key record")
	}

	legacy, err := s.legacyRecord(ctx, spaceID)
	if err != nil {
		return WrappedDK{}, err
	}
	if kind == kindRK {
		return legacy.RK, nil
	}
	return legacy.SRW, nil
}

// read reconstructs a Space's key record from its envelopes, so both stores
// report the same Status from the same facts.
func (s *StateStore) read(ctx context.Context, spaceID string) (SpaceKeys, error) {
	rk, rkSpan, rkErr := s.envelopes.Load(ctx, spaceID, kindRK)
	srw, srwSpan, srwErr := s.envelopes.Load(ctx, spaceID, kindSRW)
	for _, err := range []error{rkErr, srwErr} {
		if err != nil && !state.IsNotFound(err) {
			return SpaceKeys{}, errors.New("keys: could not read the key record")
		}
	}
	rec := SpaceKeys{
		SpaceID:   spaceID,
		CreatedAt: earliest(rkSpan.Oldest, srwSpan.Oldest),
		UpdatedAt: latest(rkSpan.Newest, srwSpan.Newest),
	}
	if rkErr == nil {
		rec.RK = rk
	}
	if srwErr == nil {
		rec.SRW = srw
	}
	if rkErr == nil && srwErr == nil {
		return rec, nil
	}

	// One or both kinds have no version yet: a Space set up before versioning,
	// or one mid-migration whose other kind has since been rotated. Status must
	// agree with what GetRK/GetSRW would return, so the same fallback applies.
	legacy, err := s.legacyRecord(ctx, spaceID)
	if err != nil {
		if rkErr != nil && srwErr != nil {
			return SpaceKeys{}, err
		}
		return rec, nil
	}
	if rkErr != nil {
		rec.RK = legacy.RK
	}
	if srwErr != nil {
		rec.SRW = legacy.SRW
	}
	rec.CreatedAt = earliest(rec.CreatedAt, legacy.CreatedAt)
	rec.UpdatedAt = latest(rec.UpdatedAt, legacy.UpdatedAt)
	return rec, nil
}

// legacyRecord reads the pre-versioned single-document record.
func (s *StateStore) legacyRecord(ctx context.Context, spaceID string) (SpaceKeys, error) {
	rec, err := s.legacy.Get(ctx, spaceID)
	if err != nil {
		if state.IsNotFound(err) {
			return SpaceKeys{}, ErrNotFound{SpaceID: spaceID}
		}
		return SpaceKeys{}, errors.New("keys: could not read the key record")
	}
	return rec, nil
}

// append writes a new envelope version. It never replaces one: see the layout
// note at the top of this file.
func (s *StateStore) append(spaceID, kind string, w WrappedDK) error {
	if spaceID == "" {
		return fmt.Errorf("keys: space id required")
	}

	ctx, cancel := s.context()
	defer cancel()

	if err := s.envelopes.Append(ctx, s.clock.Now().UTC(), cloneWrapped(w), spaceID, kind); err != nil {
		return errors.New("keys: could not store the key record")
	}
	return nil
}

func (s *StateStore) context() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), storeTimeout)
}

// mergeIDs unions two sorted id lists, keeping them sorted and distinct.
func mergeIDs(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, id := range list {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// earliest returns the first non-zero of two times, or the zero time.
func earliest(a, b time.Time) time.Time {
	switch {
	case a.IsZero():
		return b
	case b.IsZero():
		return a
	case b.Before(a):
		return b
	default:
		return a
	}
}

// latest returns the last non-zero of two times, or the zero time.
func latest(a, b time.Time) time.Time {
	switch {
	case a.IsZero():
		return b
	case b.IsZero():
		return a
	case b.After(a):
		return b
	default:
		return a
	}
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
