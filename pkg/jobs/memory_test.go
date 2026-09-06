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

func TestFinish_RecordsOutcomeInOneWrite(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)

	j, err := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup, State: StateRunning})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	clock.Advance(time.Minute)
	if err := store.Finish(ctx, j.ID, Outcome{
		State:      StateSucceeded,
		SnapshotID: "snap-1",
		FileCount:  7,
		TotalBytes: 4096,
	}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	got, err := store.Get(ctx, j.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateSucceeded || got.SnapshotID != "snap-1" {
		t.Fatalf("job = %+v", got)
	}
	if got.FileCount != 7 || got.TotalBytes != 4096 {
		t.Fatalf("counts not recorded: %+v", got)
	}
	if !got.UpdatedAt.Equal(epoch.Add(time.Minute)) || !got.FinishedAt.Equal(epoch.Add(time.Minute)) {
		t.Fatalf("timestamps = %+v", got)
	}
	if !got.CreatedAt.Equal(epoch) {
		t.Fatalf("CreatedAt changed: %v", got.CreatedAt)
	}
	if got.Duration() != time.Minute {
		t.Fatalf("Duration = %v", got.Duration())
	}
}

func TestFinish_Validation(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	var nf ErrNotFound
	if err := store.Finish(ctx, "absent", Outcome{State: StateFailed}); !errors.As(err, &nf) {
		t.Fatalf("Finish error = %v, want ErrNotFound", err)
	}

	j, _ := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup})
	if err := store.Finish(ctx, j.ID, Outcome{State: StateRunning}); !errors.Is(err, ErrNotTerminal) {
		t.Fatalf("non-terminal Finish = %v, want ErrNotTerminal", err)
	}
}

func TestCreate_DefaultsToManualTrigger(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	j, err := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if j.Trigger != TriggerManual {
		t.Fatalf("Trigger = %q, want %q", j.Trigger, TriggerManual)
	}

	scheduled, err := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup, Trigger: TriggerSchedule})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if scheduled.Trigger != TriggerSchedule {
		t.Fatalf("Trigger = %q, want %q", scheduled.Trigger, TriggerSchedule)
	}
}

func TestListRecent_AppliesLimit(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)

	var ids []string
	for range 5 {
		j, err := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		ids = append(ids, j.ID)
		clock.Advance(time.Minute)
	}

	got, err := store.ListRecent(ctx, "s1", 2)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 jobs, got %d", len(got))
	}
	if got[0].ID != ids[4] || got[1].ID != ids[3] {
		t.Fatal("ListRecent must return the newest jobs, newest first")
	}

	all, err := store.ListRecent(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("ListRecent all: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("limit 0 must mean all, got %d", len(all))
	}
}

func TestPruneBefore_KeepsRunningAndRecentJobs(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)

	old, _ := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup, State: StateRunning})
	if err := store.Finish(ctx, old.ID, Outcome{State: StateSucceeded}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	stuck, _ := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup, State: StateRunning})

	clock.Advance(48 * time.Hour)
	recent, _ := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup, State: StateRunning})
	if err := store.Finish(ctx, recent.ID, Outcome{State: StateFailed, Error: "nope"}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	removed, err := store.PruneBefore(ctx, epoch.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("PruneBefore: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}

	if _, err := store.Get(ctx, old.ID); err == nil {
		t.Fatal("finished job older than the cutoff must be pruned")
	}
	// A run still marked running is never pruned: recovery, not retention,
	// decides its fate.
	if _, err := store.Get(ctx, stuck.ID); err != nil {
		t.Fatalf("running job was pruned: %v", err)
	}
	if _, err := store.Get(ctx, recent.ID); err != nil {
		t.Fatalf("recent job was pruned: %v", err)
	}
}

func TestLastOf(t *testing.T) {
	list := []Job{
		{ID: "3", Kind: KindBackup, State: StateFailed},
		{ID: "2", Kind: KindRestore, State: StateSucceeded},
		{ID: "1", Kind: KindBackup, State: StateSucceeded},
	}

	if j, ok := LastOf(list, KindBackup, ""); !ok || j.ID != "3" {
		t.Fatalf("last backup = %+v, %v", j, ok)
	}
	if j, ok := LastOf(list, KindBackup, StateSucceeded); !ok || j.ID != "1" {
		t.Fatalf("last successful backup = %+v, %v", j, ok)
	}
	if _, ok := LastOf(list, KindPrune, ""); ok {
		t.Fatal("no prune job exists")
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
