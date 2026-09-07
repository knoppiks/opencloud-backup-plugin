package keys

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/state"
)

var keyEpoch = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// Both Store implementations must behave identically; the persistent one is
// what makes an unattended run possible after a restart.
func TestStoreContract(t *testing.T) {
	implementations := map[string]func(Clock) Store{
		"memory": func(c Clock) Store { return NewMemoryStoreWithClock(c) },
		"state":  func(c Clock) Store { return NewStateStore(state.NewMemoryStore(), c) },
	}

	for name, newImpl := range implementations {
		t.Run(name, func(t *testing.T) {
			clock := testutil.NewFakeClock(keyEpoch)
			store := newImpl(clock)

			// An unconfigured Space reports "not configured", not an error.
			status, err := store.Status("space$a!a")
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if status.Configured || status.HasRK || status.HasSRW {
				t.Fatalf("status of an unconfigured space: %+v", status)
			}

			var nf ErrNotFound
			if _, err := store.GetSRW("space$a!a"); !errors.As(err, &nf) {
				t.Fatalf("GetSRW absent: %v", err)
			}
			if _, err := store.GetRK("space$a!a"); !errors.As(err, &nf) {
				t.Fatalf("GetRK absent: %v", err)
			}

			// The wrong envelope kind must never be filed under the wrong slot.
			if err := store.PutSRW("space$a!a", WrappedDK{Kind: WrapRK, Blob: []byte("x")}); !errors.Is(err, ErrBadEnvelope) {
				t.Fatalf("PutSRW with an RK envelope = %v", err)
			}
			if err := store.PutRK("space$a!a", WrappedDK{Kind: WrapSRW, Blob: []byte("x")}); !errors.Is(err, ErrBadEnvelope) {
				t.Fatalf("PutRK with an SRW envelope = %v", err)
			}

			srw := WrappedDK{Version: EnvelopeVersion, Kind: WrapSRW, Blob: []byte("srw-blob"), CreatedAt: keyEpoch}
			if err := store.PutSRW("space$a!a", srw); err != nil {
				t.Fatalf("PutSRW: %v", err)
			}
			clock.Advance(time.Minute)
			rk := WrappedDK{Version: EnvelopeVersion, Kind: WrapRK, Blob: []byte("rk-blob"), CreatedAt: keyEpoch}
			if err := store.PutRK("space$a!a", rk); err != nil {
				t.Fatalf("PutRK: %v", err)
			}

			gotSRW, err := store.GetSRW("space$a!a")
			if err != nil {
				t.Fatalf("GetSRW: %v", err)
			}
			if !bytes.Equal(gotSRW.Blob, []byte("srw-blob")) || gotSRW.Kind != WrapSRW {
				t.Fatalf("SRW envelope = %+v", gotSRW)
			}
			gotRK, err := store.GetRK("space$a!a")
			if err != nil {
				t.Fatalf("GetRK: %v", err)
			}
			if !bytes.Equal(gotRK.Blob, []byte("rk-blob")) || gotRK.Kind != WrapRK {
				t.Fatalf("RK envelope = %+v", gotRK)
			}

			// Mutating a returned envelope must not reach into the store.
			gotRK.Blob[0] = 'X'
			again, err := store.GetRK("space$a!a")
			if err != nil {
				t.Fatalf("GetRK: %v", err)
			}
			if !bytes.Equal(again.Blob, []byte("rk-blob")) {
				t.Fatalf("stored envelope was mutated through a returned copy: %q", again.Blob)
			}

			status, err = store.Status("space$a!a")
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if !status.Configured || !status.HasRK || !status.HasSRW {
				t.Fatalf("status = %+v", status)
			}
			if status.RKVersion != EnvelopeVersion || status.SRWVersion != EnvelopeVersion {
				t.Fatalf("status versions = %+v", status)
			}
			if !status.CreatedAt.Equal(keyEpoch) {
				t.Fatalf("CreatedAt = %v", status.CreatedAt)
			}
			if !status.UpdatedAt.Equal(keyEpoch.Add(time.Minute)) {
				t.Fatalf("UpdatedAt = %v", status.UpdatedAt)
			}
		})
	}
}

// The property the whole store exists for: a write never destroys the envelope
// it supersedes. Without it, one crash mid-write makes every snapshot that
// Space ever wrote unreadable.
func TestStateStore_EnvelopesAreAppendOnly(t *testing.T) {
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(keyEpoch)
	store := NewStateStore(backing, clock)

	first := WrappedDK{Version: 1, Kind: WrapRK, Blob: []byte("rk-one")}
	if err := store.PutRK("s1", first); err != nil {
		t.Fatalf("PutRK: %v", err)
	}
	clock.Advance(time.Hour)
	if err := store.PutRK("s1", WrappedDK{Version: 1, Kind: WrapRK, Blob: []byte("rk-two")}); err != nil {
		t.Fatalf("PutRK again: %v", err)
	}

	keys, err := backing.List(t.Context(), "keyenvelopes")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("stored documents = %v, want two versions", keys)
	}

	got, err := store.GetRK("s1")
	if err != nil {
		t.Fatalf("GetRK: %v", err)
	}
	if !bytes.Equal(got.Blob, []byte("rk-two")) {
		t.Fatalf("GetRK = %q, want the newest version", got.Blob)
	}

	// The superseded envelope is still there: it is the audit trail a rotation
	// leaves, and the fallback if the newest one ever turns out to be wrong.
	old, err := backing.Get(t.Context(), keys[0])
	if err != nil {
		t.Fatalf("read superseded version: %v", err)
	}
	if !bytes.Contains(old, []byte("cmstb25l")) { // base64 of "rk-one"
		t.Fatalf("superseded version = %s", old)
	}
}

// A store that dies partway through a two-envelope setup must leave what it
// already wrote readable, and must not have damaged an earlier version.
func TestStateStore_FailedWriteLeavesThePreviousVersionReadable(t *testing.T) {
	backing := &flakyStore{Store: state.NewMemoryStore()}
	clock := testutil.NewFakeClock(keyEpoch)
	store := NewStateStore(backing, clock)

	if err := store.PutRK("s1", WrappedDK{Version: 1, Kind: WrapRK, Blob: []byte("rk-one")}); err != nil {
		t.Fatalf("PutRK: %v", err)
	}
	if err := store.PutSRW("s1", WrappedDK{Version: 1, Kind: WrapSRW, Blob: []byte("srw-one")}); err != nil {
		t.Fatalf("PutSRW: %v", err)
	}

	// The service crashes while rotating: the new RK version never lands.
	clock.Advance(time.Hour)
	backing.failWrites = true
	if err := store.PutRK("s1", WrappedDK{Version: 1, Kind: WrapRK, Blob: []byte("rk-two")}); err == nil {
		t.Fatal("PutRK: want the injected failure")
	}
	backing.failWrites = false

	got, err := store.GetRK("s1")
	if err != nil {
		t.Fatalf("GetRK after a failed write: %v", err)
	}
	if !bytes.Equal(got.Blob, []byte("rk-one")) {
		t.Fatalf("GetRK = %q, want the version that was there before", got.Blob)
	}
	status, err := store.Status("s1")
	if err != nil || !status.Configured {
		t.Fatalf("status after a failed write = %+v (%v)", status, err)
	}
}

// A deployment that predates versioning keeps working: its single-document
// record is still read, is never rewritten, and is superseded per envelope kind
// as new versions are appended.
func TestStateStore_ReadsPreVersionedRecords(t *testing.T) {
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(keyEpoch)

	legacy := state.NewDocuments[SpaceKeys](backing, "keys")
	if err := legacy.Create(t.Context(), SpaceKeys{
		SpaceID:   "s1",
		RK:        WrappedDK{Version: 1, Kind: WrapRK, Blob: []byte("old-rk")},
		SRW:       WrappedDK{Version: 1, Kind: WrapSRW, Blob: []byte("old-srw")},
		CreatedAt: keyEpoch,
		UpdatedAt: keyEpoch,
	}, "s1"); err != nil {
		t.Fatalf("seed legacy record: %v", err)
	}

	store := NewStateStore(backing, clock)
	rk, err := store.GetRK("s1")
	if err != nil || !bytes.Equal(rk.Blob, []byte("old-rk")) {
		t.Fatalf("GetRK = %q (%v), want the pre-versioned envelope", rk.Blob, err)
	}
	status, err := store.Status("s1")
	if err != nil || !status.Configured {
		t.Fatalf("status = %+v (%v)", status, err)
	}

	// Rotating the RK supersedes only the RK; the SRW still comes from the old
	// record, and the old record itself is untouched.
	clock.Advance(time.Hour)
	if err := store.PutRK("s1", WrappedDK{Version: 1, Kind: WrapRK, Blob: []byte("new-rk")}); err != nil {
		t.Fatalf("PutRK: %v", err)
	}
	rk, err = store.GetRK("s1")
	if err != nil || !bytes.Equal(rk.Blob, []byte("new-rk")) {
		t.Fatalf("GetRK = %q (%v), want the new version", rk.Blob, err)
	}
	srw, err := store.GetSRW("s1")
	if err != nil || !bytes.Equal(srw.Blob, []byte("old-srw")) {
		t.Fatalf("GetSRW = %q (%v), want the pre-versioned envelope", srw.Blob, err)
	}
	if _, err := legacy.Get(t.Context(), "s1"); err != nil {
		t.Fatalf("the pre-versioned record was disturbed: %v", err)
	}
	status, err = store.Status("s1")
	if err != nil || !status.Configured {
		t.Fatalf("status after rotation = %+v (%v)", status, err)
	}
}

// flakyStore fails writes on demand, so a crash mid-write can be simulated.
type flakyStore struct {
	state.Store
	failWrites bool
}

func (f *flakyStore) Create(ctx context.Context, key string, value []byte) error {
	if f.failWrites {
		return errors.New("state: injected write failure")
	}
	return f.Store.Create(ctx, key, value)
}

func (f *flakyStore) Replace(ctx context.Context, key string, value []byte) error {
	if f.failWrites {
		return errors.New("state: injected write failure")
	}
	return f.Store.Replace(ctx, key, value)
}

// Unattended runs depend on the SRW envelope outliving the process: without it
// a restarted service cannot back anything up until a user logs in again.
func TestStateStore_SurvivesRestart(t *testing.T) {
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(keyEpoch)

	before := NewStateStore(backing, clock)
	if err := before.PutSRW("s1", WrappedDK{Version: 1, Kind: WrapSRW, Blob: []byte("srw")}); err != nil {
		t.Fatalf("PutSRW: %v", err)
	}
	if err := before.PutRK("s1", WrappedDK{Version: 1, Kind: WrapRK, Blob: []byte("rk")}); err != nil {
		t.Fatalf("PutRK: %v", err)
	}

	after := NewStateStore(backing, clock)
	got, err := after.GetSRW("s1")
	if err != nil {
		t.Fatalf("GetSRW after restart: %v", err)
	}
	if !bytes.Equal(got.Blob, []byte("srw")) {
		t.Fatalf("SRW envelope after restart: %q", got.Blob)
	}
	status, err := after.Status("s1")
	if err != nil || !status.Configured {
		t.Fatalf("status after restart = %+v (%v)", status, err)
	}
}
