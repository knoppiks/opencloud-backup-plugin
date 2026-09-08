package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/state"
)

// newLocker builds a locker whose renewals never fire on their own, so tests
// drive them explicitly.
func newLocker(t *testing.T, backing state.Store, clock Clock, ttl time.Duration) (*LeaseLocker, chan time.Time) {
	t.Helper()

	renew := make(chan time.Time)
	locker, err := NewLeaseLocker(backing, LeaseOptions{
		TTL:   ttl,
		Clock: clock,
		After: func(time.Duration) <-chan time.Time { return renew },
	})
	if err != nil {
		t.Fatalf("NewLeaseLocker: %v", err)
	}
	return locker, renew
}

func TestLeaseLocker_ExcludesConcurrentRuns(t *testing.T) {
	ctx := context.Background()
	clock := testutil.NewFakeClock(epoch)
	locker, _ := newLocker(t, state.NewMemoryStore(), clock, time.Minute)

	release, err := locker.Acquire(ctx, "s1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, err := locker.Acquire(ctx, "s1"); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Acquire = %v, want ErrLocked", err)
	}

	releaseOther, err := locker.Acquire(ctx, "s2")
	if err != nil {
		t.Fatalf("Acquire other space: %v", err)
	}
	releaseOther()

	release()
	release() // idempotent

	if _, err := locker.Acquire(ctx, "s1"); err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
}

func TestLeaseLocker_ReleaseDropsTheDurableLease(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)
	locker, _ := newLocker(t, backing, clock, time.Minute)

	release, err := locker.Acquire(ctx, "s1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	keys, err := backing.List(ctx, leasePrefix)
	if err != nil || len(keys) != 1 {
		t.Fatalf("lease keys while held = %v (%v)", keys, err)
	}

	release()
	keys, err = backing.List(ctx, leasePrefix)
	if err != nil || len(keys) != 0 {
		t.Fatalf("lease keys after release = %v (%v)", keys, err)
	}
}

// A crashed process leaves a lease behind. Once it expires, the Space must be
// usable again — otherwise one crash wedges a Space forever.
func TestLeaseLocker_ExpiredLeaseIsTakenOver(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)

	crashed, _ := newLocker(t, backing, clock, 10*time.Minute)
	if _, err := crashed.Acquire(ctx, "s1"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// A new process: same durable state, no local hold.
	restarted, _ := newLocker(t, backing, clock, 10*time.Minute)
	if _, err := restarted.Acquire(ctx, "s1"); !errors.Is(err, ErrLocked) {
		t.Fatalf("Acquire while the lease is live = %v, want ErrLocked", err)
	}

	clock.Advance(11 * time.Minute)
	if _, err := restarted.Acquire(ctx, "s1"); err != nil {
		t.Fatalf("Acquire after the lease expired: %v", err)
	}
}

func TestLeaseLocker_RenewalExtendsTheLease(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)
	locker, renew := newLocker(t, backing, clock, 10*time.Minute)

	if _, err := locker.Acquire(ctx, "s1"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// A long run outlives its own TTL; renewal is what keeps another process
	// from concluding it crashed.
	clock.Advance(9 * time.Minute)
	renew <- clock.Now()

	deadline := time.Now().Add(2 * time.Second)
	docs := state.NewDocuments[lease](backing, leasePrefix)
	for {
		rec, err := docs.Get(ctx, "s1")
		if err != nil {
			t.Fatalf("read lease: %v", err)
		}
		if rec.ExpiresAt.Equal(epoch.Add(19 * time.Minute)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lease was not renewed: expires %v", rec.ExpiresAt)
		}
		time.Sleep(time.Millisecond)
	}
}

// The crash-simulation case: a run's process disappears mid-run. Its job must
// not stay "running" forever, and its Space must become runnable again.
func TestLeaseLocker_RecoverClosesOutAbandonedRuns(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)
	store := NewStateStore(backing, clock)

	crashed, _ := newLocker(t, backing, clock, 10*time.Minute)
	if _, err := crashed.Acquire(ctx, "s1"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	abandoned, err := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup, State: StateRunning})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	restarted, _ := newLocker(t, backing, clock, 10*time.Minute)

	// While the lease is live, recovery must keep its hands off.
	if n, err := restarted.Recover(ctx, store); err != nil || n != 0 {
		t.Fatalf("Recover with a live lease = %d, %v", n, err)
	}
	if j, _ := store.Get(ctx, abandoned.ID); j.State != StateRunning {
		t.Fatalf("job state = %q, want it untouched", j.State)
	}

	clock.Advance(11 * time.Minute)
	n, err := restarted.Recover(ctx, store)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if n != 1 {
		t.Fatalf("recovered = %d, want 1", n)
	}

	got, err := store.Get(ctx, abandoned.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateFailed || got.Error != MessageInterrupted {
		t.Fatalf("recovered job = %+v", got)
	}
	if keys, _ := backing.List(ctx, leasePrefix); len(keys) != 0 {
		t.Fatalf("expired lease was not dropped: %v", keys)
	}

	// And the Space runs again.
	if _, err := restarted.Acquire(ctx, "s1"); err != nil {
		t.Fatalf("Acquire after recovery: %v", err)
	}
}

func TestLeaseLocker_RecoverLeavesThisProcessAlone(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)
	store := NewStateStore(backing, clock)
	locker, _ := newLocker(t, backing, clock, 10*time.Minute)

	if _, err := locker.Acquire(ctx, "s1"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	live, _ := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup, State: StateRunning})

	// The scheduler calls Recover on every tick; a long run whose lease has not
	// been renewed yet must not be killed by its own process.
	clock.Advance(11 * time.Minute)
	if n, err := locker.Recover(ctx, store); err != nil || n != 0 {
		t.Fatalf("Recover = %d, %v; want it to skip locally held spaces", n, err)
	}
	if j, _ := store.Get(ctx, live.ID); j.State != StateRunning {
		t.Fatalf("own live run was failed: %+v", j)
	}
}

// The gate an operator key rotation stands behind: rotating while a run holds a
// Space would re-wrap an envelope that run is about to open.
func TestActiveLeasesCountsOnlyLiveOnes(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)
	locker, _ := newLocker(t, backing, clock, time.Minute)

	if active, err := ActiveLeases(ctx, backing, clock.Now()); err != nil || active != 0 {
		t.Fatalf("ActiveLeases with no runs = %d (%v)", active, err)
	}

	release, err := locker.Acquire(ctx, "s1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if active, err := ActiveLeases(ctx, backing, clock.Now()); err != nil || active != 1 {
		t.Fatalf("ActiveLeases during a run = %d (%v)", active, err)
	}

	// An expired lease means the holder is gone, not that a run is live.
	clock.Advance(2 * time.Minute)
	if active, err := ActiveLeases(ctx, backing, clock.Now()); err != nil || active != 0 {
		t.Fatalf("ActiveLeases after expiry = %d (%v)", active, err)
	}

	release()
	if active, err := ActiveLeases(ctx, backing, clock.Now()); err != nil || active != 0 {
		t.Fatalf("ActiveLeases after release = %d (%v)", active, err)
	}
}

// An unreadable lease document is not evidence that the service is idle.
func TestActiveLeasesCountsUnreadableLeases(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	if err := backing.Create(ctx, "leases/s1", []byte("{not json")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	active, err := ActiveLeases(ctx, backing, epoch)
	if err != nil {
		t.Fatalf("ActiveLeases: %v", err)
	}
	if active != 1 {
		t.Fatalf("ActiveLeases = %d, want the unreadable lease counted", active)
	}
}

func TestLeaseLocker_RequiresSpaceID(t *testing.T) {
	locker, _ := newLocker(t, state.NewMemoryStore(), testutil.NewFakeClock(epoch), time.Minute)
	if _, err := locker.Acquire(context.Background(), ""); err == nil {
		t.Fatal("empty space id must be rejected")
	}
}

func TestNewLeaseLocker_RequiresState(t *testing.T) {
	if _, err := NewLeaseLocker(nil, LeaseOptions{}); err == nil {
		t.Fatal("nil state store must be rejected")
	}
}

// The wedge this exists for: a run that ended, released its lock, and then
// failed to record its outcome. There is no lease left to expire, so the pass
// above cannot see it, and every later scheduler tick sees a run in progress
// that does not exist.
func TestLeaseLocker_RecoverClosesOutARunWithNoLockBehindIt(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)
	store := NewStateStore(backing, clock)

	locker, err := NewLeaseLocker(backing, LeaseOptions{
		TTL:         10 * time.Minute,
		OrphanSweep: time.Hour,
		Clock:       clock,
		After:       func(time.Duration) <-chan time.Time { return make(chan time.Time) },
	})
	if err != nil {
		t.Fatalf("NewLeaseLocker: %v", err)
	}

	// A run whose lock is already gone: exactly what a lost Finish leaves.
	orphan, err := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup, State: StateRunning})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The sweep reads job documents, so it is not run at startup and not run on
	// every tick.
	if n, err := locker.Recover(ctx, store); err != nil || n != 0 {
		t.Fatalf("Recover before the sweep is due = %d, %v", n, err)
	}
	if j, _ := store.Get(ctx, orphan.ID); j.State != StateRunning {
		t.Fatalf("job state = %q, want it untouched", j.State)
	}

	clock.Advance(61 * time.Minute)
	n, err := locker.Recover(ctx, store)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if n != 1 {
		t.Fatalf("recovered = %d, want 1", n)
	}
	got, _ := store.Get(ctx, orphan.ID)
	if got.State != StateFailed || got.Error != MessageInterrupted {
		t.Fatalf("recovered job = %+v", got)
	}
}

// Absence of a lease is only evidence once. A run genuinely under way is either
// held in this process or covered by a live lease, and a run takes its lock
// before it creates its job record — so there is no window in which a
// legitimate run looks orphaned.
func TestLeaseLocker_RecoverSparesRunsThatAreStillHeld(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)
	store := NewStateStore(backing, clock)

	opts := LeaseOptions{
		TTL:         2 * time.Hour, // longer than the sweep interval below
		OrphanSweep: time.Hour,
		Clock:       clock,
		After:       func(time.Duration) <-chan time.Time { return make(chan time.Time) },
	}

	mine, err := NewLeaseLocker(backing, opts)
	if err != nil {
		t.Fatalf("NewLeaseLocker: %v", err)
	}
	elsewhere, err := NewLeaseLocker(backing, opts)
	if err != nil {
		t.Fatalf("NewLeaseLocker: %v", err)
	}

	if _, err := mine.Acquire(ctx, "s1"); err != nil {
		t.Fatalf("Acquire local: %v", err)
	}
	if _, err := elsewhere.Acquire(ctx, "s2"); err != nil {
		t.Fatalf("Acquire remote: %v", err)
	}
	local, _ := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup, State: StateRunning})
	remote, _ := store.Create(ctx, Job{SpaceID: "s2", Kind: KindRestore, State: StateRunning})

	clock.Advance(61 * time.Minute)
	if n, err := mine.Recover(ctx, store); err != nil || n != 0 {
		t.Fatalf("Recover = %d, %v; want both runs spared", n, err)
	}
	for _, id := range []string{local.ID, remote.ID} {
		if j, _ := store.Get(ctx, id); j.State != StateRunning {
			t.Fatalf("live run %s was failed: %+v", id, j)
		}
	}
}

// Busy is what lets the scheduler ask "is something already running here"
// without reading run history. It must count every kind of run and must not
// count a lease whose holder is gone.
func TestLeaseLocker_Busy(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)
	locker, _ := newLocker(t, backing, clock, 10*time.Minute)

	if busy, err := locker.Busy(ctx, "s1"); err != nil || busy {
		t.Fatalf("Busy with no run = %v (%v)", busy, err)
	}

	release, err := locker.Acquire(ctx, "s1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if busy, err := locker.Busy(ctx, "s1"); err != nil || !busy {
		t.Fatalf("Busy while held = %v (%v)", busy, err)
	}

	// Another process's live lease counts; its expiry does not.
	observer, _ := newLocker(t, backing, clock, 10*time.Minute)
	if busy, err := observer.Busy(ctx, "s1"); err != nil || !busy {
		t.Fatalf("Busy for another process's live lease = %v (%v)", busy, err)
	}
	clock.Advance(11 * time.Minute)
	if busy, err := observer.Busy(ctx, "s1"); err != nil || busy {
		t.Fatalf("Busy for an expired lease = %v (%v)", busy, err)
	}

	release()
	if busy, err := locker.Busy(ctx, "s1"); err != nil || busy {
		t.Fatalf("Busy after release = %v (%v)", busy, err)
	}
}
