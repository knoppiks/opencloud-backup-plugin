// Package keystore is the durable keys.Store, on top of internal/state.
//
// It lives outside pkg/keys because it adapts the service's own storage, not
// the key format: pkg/keys is a public reference implementation (decisions.md
// #26) and must not name internal types in its API.
package keystore

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
// Each operation runs on the caller's context, bounded by storeTimeout so a
// caller without a deadline of its own still cannot wait forever.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/knoppiks/opencloud-backup-plugin/internal/state"
	"github.com/knoppiks/opencloud-backup-plugin/pkg/keys"
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

// What a state failure is reported as. The state store's own error is wrapped
// beneath, so the operator's log says why (review-2026-10.md F3): it names a
// document key and a transport failure, never a document's contents — the
// state store does not quote what it read, and a malformed document is
// reported by key alone (state.ErrMalformed).
var (
	errReadRecord  = errors.New("keystore: could not read the key record")
	errListRecords = errors.New("keystore: could not list the key records")
	errStoreRecord = errors.New("keystore: could not store the key record")
)

// storeTimeout bounds a single state operation.
const storeTimeout = 30 * time.Second

// Store is a keys.Store backed by durable state.
type Store struct {
	envelopes *state.Versions[keys.WrappedDK]
	legacy    *state.Documents[keys.SpaceKeys]
	clock     keys.Clock
}

var _ keys.Store = (*Store)(nil)

// New returns a Store persisting to st. A nil clock is the system clock.
func New(st state.Store, clock keys.Clock) *Store {
	if clock == nil {
		clock = systemClock{}
	}
	return &Store{
		envelopes: state.NewVersions[keys.WrappedDK](st, envelopePrefix),
		legacy:    state.NewDocuments[keys.SpaceKeys](st, legacyKeysPrefix),
		clock:     clock,
	}
}

// PutSRW stores the SRW-wrapped DK for a space.
func (s *Store) PutSRW(ctx context.Context, spaceID string, w keys.WrappedDK) error {
	if w.Kind != keys.WrapSRW {
		return keys.ErrBadEnvelope
	}
	return s.append(ctx, spaceID, kindSRW, w)
}

// GetSRW returns the SRW-wrapped DK for a space.
func (s *Store) GetSRW(ctx context.Context, spaceID string) (keys.WrappedDK, error) {
	return s.newest(ctx, spaceID, kindSRW, keys.WrapSRW)
}

// PutRK stores the RK-wrapped DK for a space.
func (s *Store) PutRK(ctx context.Context, spaceID string, w keys.WrappedDK) error {
	if w.Kind != keys.WrapRK {
		return keys.ErrBadEnvelope
	}
	return s.append(ctx, spaceID, kindRK, w)
}

// GetRK returns the RK-wrapped DK for a space.
func (s *Store) GetRK(ctx context.Context, spaceID string) (keys.WrappedDK, error) {
	return s.newest(ctx, spaceID, kindRK, keys.WrapRK)
}

// Status reports setup state for a space without revealing key material.
func (s *Store) Status(ctx context.Context, spaceID string) (keys.Status, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()

	rec, err := s.read(ctx, spaceID)
	if err != nil {
		var notFound keys.ErrNotFound
		if errors.As(err, &notFound) {
			// "No keys yet" is a normal answer for a Space that has not been
			// set up, not an error.
			return keys.Status{SpaceID: spaceID}, nil
		}
		return keys.Status{}, err
	}
	return rec.Status(), nil
}

// Spaces returns the ids of every space holding an envelope, in order. It reads
// no documents: the ids are in the keys.
func (s *Store) Spaces(ctx context.Context) ([]string, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()

	// The versioned and pre-versioned layouts are separate collections here
	// (they hold different document types), so both are asked and the union is
	// reported: a Space set up before versioning must not be skipped by a
	// rotation.
	versioned, err := s.envelopes.IDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errListRecords, err)
	}
	legacy, err := s.legacy.IDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errListRecords, err)
	}
	return mergeIDs(versioned, legacy), nil
}

// newest returns a Space's current envelope of one kind.
func (s *Store) newest(ctx context.Context, spaceID, kind string, want keys.WrapKind) (keys.WrappedDK, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()

	w, err := s.envelope(ctx, spaceID, kind)
	if err != nil {
		return keys.WrappedDK{}, err
	}
	if w.Kind != want {
		return keys.WrappedDK{}, keys.ErrNotFound{SpaceID: spaceID}
	}
	return w.Clone(), nil
}

// envelope reads one kind of envelope, falling back to the pre-versioned record.
func (s *Store) envelope(ctx context.Context, spaceID, kind string) (keys.WrappedDK, error) {
	w, _, err := s.envelopes.Load(ctx, spaceID, kind)
	switch {
	case err == nil:
		return w, nil
	case state.IsNotFound(err):
	default:
		return keys.WrappedDK{}, fmt.Errorf("%w: %w", errReadRecord, err)
	}

	legacy, err := s.legacyRecord(ctx, spaceID)
	if err != nil {
		return keys.WrappedDK{}, err
	}
	if kind == kindRK {
		return legacy.RK, nil
	}
	return legacy.SRW, nil
}

// read reconstructs a Space's key record from its envelopes, so both stores
// report the same Status from the same facts.
func (s *Store) read(ctx context.Context, spaceID string) (keys.SpaceKeys, error) {
	rk, rkSpan, rkErr := s.envelopes.Load(ctx, spaceID, kindRK)
	srw, srwSpan, srwErr := s.envelopes.Load(ctx, spaceID, kindSRW)
	for _, err := range []error{rkErr, srwErr} {
		if err != nil && !state.IsNotFound(err) {
			return keys.SpaceKeys{}, fmt.Errorf("%w: %w", errReadRecord, err)
		}
	}
	rec := keys.SpaceKeys{
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
			return keys.SpaceKeys{}, err
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
func (s *Store) legacyRecord(ctx context.Context, spaceID string) (keys.SpaceKeys, error) {
	rec, err := s.legacy.Get(ctx, spaceID)
	if err != nil {
		if state.IsNotFound(err) {
			return keys.SpaceKeys{}, keys.ErrNotFound{SpaceID: spaceID}
		}
		return keys.SpaceKeys{}, fmt.Errorf("%w: %w", errReadRecord, err)
	}
	return rec, nil
}

// append writes a new envelope version. It never replaces one: see the layout
// note at the top of this file.
func (s *Store) append(ctx context.Context, spaceID, kind string, w keys.WrappedDK) error {
	if spaceID == "" {
		return fmt.Errorf("keystore: space id required")
	}

	ctx, cancel := bounded(ctx)
	defer cancel()

	if err := s.envelopes.Append(ctx, s.clock.Now().UTC(), w.Clone(), spaceID, kind); err != nil {
		return fmt.Errorf("%w: %w", errStoreRecord, err)
	}
	return nil
}

// bounded gives one store operation its deadline: the caller's, or
// storeTimeout, whichever comes first. The caller's cancellation always
// applies — a request that is gone or a run that is stopping does not keep a
// state read going (review-2026-10.md G5).
func bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, storeTimeout)
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

// systemClock is the wall clock.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
