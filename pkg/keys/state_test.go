package keys

import (
	"bytes"
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
