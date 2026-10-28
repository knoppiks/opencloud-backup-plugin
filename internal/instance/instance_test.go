package instance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/knoppiks/opencloud-backup-plugin/internal/state"
	"github.com/knoppiks/opencloud-backup-plugin/internal/state/statetest"
	"github.com/knoppiks/opencloud-backup-plugin/internal/testutil"
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

// newWatcher builds a guard whose waits are reported on waits and end when
// the test sends on tick, so a test knows when Claim has started watching.
func newWatcher(t *testing.T, backing state.Store, clock Clock, id string) (g *Guard, waits <-chan time.Duration, tick chan<- time.Time) {
	t.Helper()
	w := make(chan time.Duration, 16)
	c := make(chan time.Time)
	g, err := New(backing, Options{TTL: time.Minute, ID: id, Clock: clock,
		After: func(d time.Duration) <-chan time.Time { w <- d; return c }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g, w, c
}

// claimAsync runs Claim in the background, for a Claim that waits on a live
// record until the test moves time on.
func claimAsync(ctx context.Context, g *Guard) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := g.Claim(ctx)
		done <- err
	}()
	return done
}

// result waits for a background Claim.
func result(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Claim did not return")
		return nil
	}
}

// A live instance renews its record while the newcomer watches it, and that
// is what gets the newcomer refused.
func TestGuard_RefusesToStartBesideALiveInstance(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)

	first, renewFirst := newGuard(t, backing, clock, "instance-a", time.Minute)
	release, err := first.Claim(ctx)
	if err != nil {
		t.Fatalf("first Claim: %v", err)
	}

	second, waits, watch := newWatcher(t, backing, clock, "instance-b")
	done := claimAsync(ctx, second)
	<-waits

	clock.Advance(20 * time.Second)
	renewFirst <- clock.Now()
	waitForExpiry(t, backing, "instance-a", clock.Now().Add(time.Minute))
	watch <- clock.Now()
	if err := result(t, done); !errors.Is(err, ErrAnotherInstance) {
		t.Fatalf("second Claim = %v, want ErrAnotherInstance", err)
	}

	// This is the rolling-update case: the first instance stands down, the
	// replacement starts immediately rather than waiting out the TTL.
	release()
	if _, err := second.Claim(ctx); err != nil {
		t.Fatalf("Claim after the previous instance released: %v", err)
	}
}

// After an unclean stop the record outlives its process. The replacement
// waits it out instead of failing (#152), re-reading it in between.
func TestGuard_WaitsOutARecordThatIsNotRenewed(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)

	crashed, _ := newGuard(t, backing, clock, "instance-a", time.Minute)
	if _, err := crashed.Claim(ctx); err != nil {
		t.Fatalf("first Claim: %v", err)
	}

	next, waits, watch := newWatcher(t, backing, clock, "instance-b")
	done := claimAsync(ctx, next)

	// Re-read every TTL/12 while there is that much left, then once at expiry.
	if d := <-waits; d != 5*time.Second {
		t.Fatalf("first wait = %v, want TTL/12", d)
	}
	clock.Advance(57 * time.Second)
	watch <- clock.Now()
	if d := <-waits; d != 3*time.Second {
		t.Fatalf("last wait = %v, want the 3s left", d)
	}
	clock.Advance(3 * time.Second)
	watch <- clock.Now()
	if err := result(t, done); err != nil {
		t.Fatalf("Claim after the record expired = %v", err)
	}

	ids, err := state.NewDocuments[record](backing, prefix).IDs(ctx)
	if err != nil || len(ids) != 1 || ids[0] != "instance-b" {
		t.Fatalf("instance records = %v (%v), want only instance-b", ids, err)
	}
}

// A record withdrawn while it is watched frees the way at once.
func TestGuard_StartsWhenTheWatchedRecordIsWithdrawn(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)

	first, _ := newGuard(t, backing, clock, "instance-a", time.Minute)
	release, err := first.Claim(ctx)
	if err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	second, waits, watch := newWatcher(t, backing, clock, "instance-b")
	done := claimAsync(ctx, second)
	<-waits

	release()
	watch <- clock.Now()
	if err := result(t, done); err != nil {
		t.Fatalf("Claim after the record was withdrawn = %v", err)
	}
}

// Shutting down while waiting ends the wait.
func TestGuard_WaitEndsWithTheContext(t *testing.T) {
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)

	first, _ := newGuard(t, backing, clock, "instance-a", time.Minute)
	if _, err := first.Claim(context.Background()); err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	second, waits, _ := newWatcher(t, backing, clock, "instance-b")
	ctx, cancel := context.WithCancel(context.Background())
	done := claimAsync(ctx, second)
	<-waits
	cancel()
	if err := result(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("Claim = %v, want context.Canceled", err)
	}
}

// A watched record that becomes unreadable is not evidence that its instance
// is gone.
func TestGuard_RefusesWhenTheWatchedRecordBecomesUnreadable(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)

	first, _ := newGuard(t, backing, clock, "instance-a", time.Minute)
	if _, err := first.Claim(ctx); err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	second, waits, watch := newWatcher(t, backing, clock, "instance-b")
	done := claimAsync(ctx, second)
	<-waits

	key := state.NewDocuments[record](backing, prefix).Key("instance-a")
	if err := backing.Replace(ctx, key, []byte("not json")); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	watch <- clock.Now()
	if err := result(t, done); !errors.Is(err, ErrAnotherInstance) {
		t.Fatalf("Claim = %v, want ErrAnotherInstance", err)
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

// Standing down runs at shutdown, after the context the record was claimed
// with is gone. Withdrawing the record must not depend on it, or every restart
// waits out the TTL behind "another instance is running".
func TestGuard_StandsDownAfterTheClaimContextEnded(t *testing.T) {
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)
	g, _ := newGuard(t, statetest.HonoursContext(backing), clock, "instance-a", time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	standDown, err := g.Claim(ctx)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	cancel()
	standDown()

	ids, err := state.NewDocuments[record](backing, prefix).IDs(context.Background())
	if err != nil || len(ids) != 0 {
		t.Fatalf("instance records after standing down = %v (%v), want none", ids, err)
	}
}
