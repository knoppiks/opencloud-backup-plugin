package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"opencloud-backup-plugin/internal/testutil"
)

var epoch = time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

func newStore(t *testing.T) (*MemoryStore, *testutil.FakeClock) {
	t.Helper()
	clock := testutil.NewFakeClock(epoch)
	return NewMemoryStoreWithClock(clock), clock
}

func TestCreate_GeneratesIDAndTimestamps(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	j, err := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if j.ID == "" {
		t.Fatal("id must be generated")
	}
	if j.State != StatePending {
		t.Fatalf("default state = %q, want pending", j.State)
	}
	if !j.CreatedAt.Equal(epoch) || !j.UpdatedAt.Equal(epoch) {
		t.Fatalf("timestamps = %+v", j)
	}

	// Generated ids must be unique.
	other, err := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup})
	if err != nil {
		t.Fatalf("Create 2: %v", err)
	}
	if other.ID == j.ID {
		t.Fatal("generated ids must be unique")
	}
}

func TestCreate_Validation(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	if _, err := store.Create(ctx, Job{Kind: KindBackup}); err == nil {
		t.Fatal("missing space id must be rejected")
	}
	if _, err := store.Create(ctx, Job{SpaceID: "s1"}); err == nil {
		t.Fatal("missing kind must be rejected")
	}

	if _, err := store.Create(ctx, Job{ID: "fixed", SpaceID: "s1", Kind: KindBackup}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Create(ctx, Job{ID: "fixed", SpaceID: "s1", Kind: KindBackup}); err == nil {
		t.Fatal("duplicate id must be rejected")
	}
}

func TestUpdateState_AndSnapshotID(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)

	j, err := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup, State: StateRunning})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	clock.Advance(time.Minute)
	if err := store.SetSnapshotID(ctx, j.ID, "snap-1"); err != nil {
		t.Fatalf("SetSnapshotID: %v", err)
	}
	if err := store.UpdateState(ctx, j.ID, StateSucceeded, ""); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}

	got, err := store.Get(ctx, j.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateSucceeded || got.SnapshotID != "snap-1" {
		t.Fatalf("job = %+v", got)
	}
	if !got.UpdatedAt.Equal(epoch.Add(time.Minute)) {
		t.Fatalf("UpdatedAt = %v", got.UpdatedAt)
	}
	if !got.CreatedAt.Equal(epoch) {
		t.Fatalf("CreatedAt changed: %v", got.CreatedAt)
	}
}

func TestUpdateState_Validation(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	var nf ErrNotFound
	if err := store.UpdateState(ctx, "absent", StateFailed, "x"); !errors.As(err, &nf) {
		t.Fatalf("UpdateState error = %v, want ErrNotFound", err)
	}
	if err := store.SetSnapshotID(ctx, "absent", "s"); !errors.As(err, &nf) {
		t.Fatalf("SetSnapshotID error = %v, want ErrNotFound", err)
	}

	j, _ := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup})
	if err := store.UpdateState(ctx, j.ID, "", ""); err == nil {
		t.Fatal("empty state must be rejected")
	}
}

func TestGet_NotFound(t *testing.T) {
	store, _ := newStore(t)
	var nf ErrNotFound
	if _, err := store.Get(context.Background(), "absent"); !errors.As(err, &nf) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
}

func TestList_ScopedToSpaceNewestFirst(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)

	first, _ := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup})
	clock.Advance(time.Minute)
	second, _ := store.Create(ctx, Job{SpaceID: "s1", Kind: KindPrune})
	if _, err := store.Create(ctx, Job{SpaceID: "s2", Kind: KindBackup}); err != nil {
		t.Fatalf("Create other space: %v", err)
	}

	got, err := store.List(ctx, "s1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 jobs for s1, got %d", len(got))
	}
	if got[0].ID != second.ID || got[1].ID != first.ID {
		t.Fatal("List must return newest first")
	}
}

func TestState_Terminal(t *testing.T) {
	for _, s := range []State{StateSucceeded, StateFailed} {
		if !s.Terminal() {
			t.Fatalf("%s must be terminal", s)
		}
	}
	for _, s := range []State{StatePending, StateRunning} {
		if s.Terminal() {
			t.Fatalf("%s must not be terminal", s)
		}
	}
}

// Concurrent runs for the same Space must be prevented (phase-4 testing rule).
func TestLocker_ExcludesConcurrentRunsPerSpace(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	release, err := store.Acquire(ctx, "s1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, err := store.Acquire(ctx, "s1"); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Acquire = %v, want ErrLocked", err)
	}

	// A different Space is unaffected.
	releaseOther, err := store.Acquire(ctx, "s2")
	if err != nil {
		t.Fatalf("Acquire other space: %v", err)
	}
	releaseOther()

	release()
	release() // idempotent

	if _, err := store.Acquire(ctx, "s1"); err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
}

func TestLocker_RequiresSpaceID(t *testing.T) {
	store, _ := newStore(t)
	if _, err := store.Acquire(context.Background(), ""); err == nil {
		t.Fatal("empty space id must be rejected")
	}
}

func TestLocker_OnlyOneWinnerUnderContention(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	const goroutines = 32
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		acquired int
		releases []func()
	)
	wg.Add(goroutines)
	start := make(chan struct{})
	for range goroutines {
		go func() {
			defer wg.Done()
			<-start
			release, err := store.Acquire(ctx, "s1")
			if err != nil {
				return
			}
			mu.Lock()
			acquired++
			releases = append(releases, release)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	if acquired != 1 {
		t.Fatalf("%d goroutines acquired the lock, want exactly 1", acquired)
	}
	for _, r := range releases {
		r()
	}
}
