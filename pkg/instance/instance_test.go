package instance

import (
	"context"
	"errors"
	"testing"
	"time"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/state"
)

var epoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// newGuard builds a guard whose renewals never fire on their own, so tests drive
// them explicitly.
func newGuard(t *testing.T, backing state.Store, clock Clock, id string, ttl time.Duration) (*Guard, chan time.Time) {
	t.Helper()
	renew := make(chan time.Time)
	g, err := New(backing, Options{
		TTL:   ttl,
		ID:    id,
		Clock: clock,
		After: func(time.Duration) <-chan time.Time { return renew },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g, renew
}

func TestNew_RequiresAStore(t *testing.T) {
	if _, err := New(nil, Options{}); err == nil {
		t.Fatal("a nil store must be rejected")
	}
}

func TestGuard_RefusesToStartBesideALiveInstance(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)

	first, _ := newGuard(t, backing, clock, "instance-a", time.Minute)
	release, err := first.Claim(ctx)
	if err != nil {
		t.Fatalf("first Claim: %v", err)
	}

	second, _ := newGuard(t, backing, clock, "instance-b", time.Minute)
	if _, err := second.Claim(ctx); !errors.Is(err, ErrAnotherInstance) {
		t.Fatalf("second Claim = %v, want ErrAnotherInstance", err)
	}

	// This is the rolling-update case: the first instance stands down, the
	// replacement starts immediately rather than waiting out the TTL.
	release()
	if _, err := second.Claim(ctx); err != nil {
		t.Fatalf("Claim after the previous instance released: %v", err)
	}
}

func TestGuard_TakesOverFromAnExpiredRecord(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)

	crashed, _ := newGuard(t, backing, clock, "instance-a", time.Minute)
	if _, err := crashed.Claim(ctx); err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	// The process disappears without releasing; only time frees the record.
	clock.Advance(2 * time.Minute)

	next, _ := newGuard(t, backing, clock, "instance-b", time.Minute)
	if _, err := next.Claim(ctx); err != nil {
		t.Fatalf("Claim after a crash: %v", err)
	}

	// The dead instance's record is cleared, not merely ignored.
	docs := state.NewDocuments[record](backing, prefix)
	ids, err := docs.IDs(ctx)
	if err != nil {
		t.Fatalf("IDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != "instance-b" {
		t.Fatalf("instance records = %v, want only instance-b", ids)
	}
}

func TestGuard_RenewalKeepsTheRecordLive(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)

	held, renew := newGuard(t, backing, clock, "instance-a", time.Minute)
	if _, err := held.Claim(ctx); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	clock.Advance(90 * time.Second)
	renew <- clock.Now()
	waitForExpiry(t, backing, "instance-a", clock.Now().Add(time.Minute))

	other, _ := newGuard(t, backing, clock, "instance-b", time.Minute)
	if _, err := other.Claim(ctx); !errors.Is(err, ErrAnotherInstance) {
		t.Fatalf("Claim beside a renewed instance = %v, want ErrAnotherInstance", err)
	}
}

// Reclaiming with the same id is how a restart of the same identity behaves; it
// must not lock itself out.
func TestGuard_IgnoresItsOwnRecord(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)

	g, _ := newGuard(t, backing, clock, "instance-a", time.Minute)
	if _, err := g.Claim(ctx); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	same, _ := newGuard(t, backing, clock, "instance-a", time.Minute)
	if _, err := same.Claim(ctx); err != nil {
		t.Fatalf("re-Claim with the same id: %v", err)
	}
	if g.ID() != "instance-a" {
		t.Fatalf("ID = %q", g.ID())
	}
}

// A record that cannot be decoded is not evidence that nobody is running.
func TestGuard_RefusesWhenARecordIsUnreadable(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	if err := backing.Create(ctx, prefix+"/garbage", []byte("not json")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	g, _ := newGuard(t, backing, testutil.NewFakeClock(epoch), "instance-a", time.Minute)
	if _, err := g.Claim(ctx); !errors.Is(err, ErrAnotherInstance) {
		t.Fatalf("Claim = %v, want ErrAnotherInstance", err)
	}
}

// waitForExpiry asserts the stored record carries the expected expiry, allowing
// for the renewal goroutine to get there.
func waitForExpiry(t *testing.T, backing state.Store, id string, want time.Time) {
	t.Helper()
	docs := state.NewDocuments[record](backing, prefix)
	deadline := time.Now().Add(2 * time.Second)
	for {
		rec, err := docs.Get(context.Background(), id)
		if err == nil && rec.ExpiresAt.Equal(want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("record for %s was not renewed to %v (last err %v)", id, want, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
