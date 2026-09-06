package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/state"
)

// The two Store implementations must behave identically: the memory store is
// the double the runner tests use, so any divergence would mean the tests prove
// something about code that never runs in production.
func TestStoreContract(t *testing.T) {
	implementations := map[string]func(Clock) Store{
		"memory": func(c Clock) Store { return NewMemoryStoreWithClock(c) },
		"state":  func(c Clock) Store { return NewStateStore(state.NewMemoryStore(), c) },
	}

	for name, newImpl := range implementations {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			clock := testutil.NewFakeClock(epoch)
			store := newImpl(clock)

			j, err := store.Create(ctx, Job{SpaceID: "space$a!a", Kind: KindBackup, State: StateRunning})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if j.ID == "" || !j.CreatedAt.Equal(epoch) {
				t.Fatalf("Create: %+v", j)
			}

			got, err := store.Get(ctx, j.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.ID != j.ID || got.SpaceID != "space$a!a" {
				t.Fatalf("Get: %+v", got)
			}

			var nf ErrNotFound
			if _, err := store.Get(ctx, "absent"); !errors.As(err, &nf) {
				t.Fatalf("Get absent: %v", err)
			}
			if err := store.Finish(ctx, "absent", Outcome{State: StateFailed}); !errors.As(err, &nf) {
				t.Fatalf("Finish absent: %v", err)
			}
			if err := store.Finish(ctx, j.ID, Outcome{State: StateRunning}); !errors.Is(err, ErrNotTerminal) {
				t.Fatalf("Finish non-terminal: %v", err)
			}

			clock.Advance(time.Minute)
			if _, err := store.Create(ctx, Job{SpaceID: "other", Kind: KindBackup}); err != nil {
				t.Fatalf("Create other space: %v", err)
			}
			clock.Advance(time.Minute)
			newer, err := store.Create(ctx, Job{SpaceID: "space$a!a", Kind: KindRestore})
			if err != nil {
				t.Fatalf("Create newer: %v", err)
			}

			list, err := store.List(ctx, "space$a!a")
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(list) != 2 {
				t.Fatalf("List returned %d jobs, want the space's 2", len(list))
			}
			if list[0].ID != newer.ID {
				t.Fatal("List must return newest first")
			}

			recent, err := store.ListRecent(ctx, "space$a!a", 1)
			if err != nil {
				t.Fatalf("ListRecent: %v", err)
			}
			if len(recent) != 1 || recent[0].ID != newer.ID {
				t.Fatalf("ListRecent: %+v", recent)
			}

			if err := store.Finish(ctx, j.ID, Outcome{
				State:      StateSucceeded,
				SnapshotID: "snap",
				FileCount:  3,
				TotalBytes: 300,
			}); err != nil {
				t.Fatalf("Finish: %v", err)
			}
			done, err := store.Get(ctx, j.ID)
			if err != nil {
				t.Fatalf("Get finished: %v", err)
			}
			if done.State != StateSucceeded || done.SnapshotID != "snap" || done.FileCount != 3 || done.TotalBytes != 300 {
				t.Fatalf("finished job: %+v", done)
			}
			if done.FinishedAt.IsZero() {
				t.Fatal("FinishedAt must be set")
			}

			removed, err := store.PruneBefore(ctx, epoch.Add(time.Hour))
			if err != nil {
				t.Fatalf("PruneBefore: %v", err)
			}
			// Only the finished one goes; the two unfinished jobs stay.
			if removed != 1 {
				t.Fatalf("removed = %d, want 1", removed)
			}
			if _, err := store.Get(ctx, j.ID); !errors.As(err, &nf) {
				t.Fatalf("pruned job still readable: %v", err)
			}
			if _, err := store.Get(ctx, newer.ID); err != nil {
				t.Fatalf("unfinished job was pruned: %v", err)
			}
		})
	}
}

func TestStateStore_SurvivesRestart(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)

	before := NewStateStore(backing, clock)
	j, err := before.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup, State: StateRunning, Trigger: TriggerSchedule})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := before.Finish(ctx, j.ID, Outcome{State: StateSucceeded, SnapshotID: "snap-1"}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// A fresh process, same durable state: history and the id index must both
	// be reconstructible.
	after := NewStateStore(backing, clock)
	got, err := after.Get(ctx, j.ID)
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if got.SnapshotID != "snap-1" || got.Trigger != TriggerSchedule || got.State != StateSucceeded {
		t.Fatalf("job after restart: %+v", got)
	}

	list, err := after.List(ctx, "s1")
	if err != nil {
		t.Fatalf("List after restart: %v", err)
	}
	if len(list) != 1 || list[0].ID != j.ID {
		t.Fatalf("history after restart: %+v", list)
	}
}

func TestStateStore_KeysAreChronological(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)
	store := NewStateStore(backing, clock)

	var ids []string
	for range 3 {
		j, err := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		ids = append(ids, j.ID)
		clock.Advance(time.Hour)
	}

	// Lexical key order is what makes "the newest N runs" cheap; assert it
	// directly, because the ListRecent implementation depends on it.
	keys, err := backing.List(ctx, "jobs")
	if err != nil {
		t.Fatalf("List keys: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("keys = %v", keys)
	}
	for i, id := range ids {
		if !hasSuffix(keys[i], "-"+id) {
			t.Fatalf("key %d = %q, want it to end in the %s-th job id", i, keys[i], id)
		}
	}
}

func TestStateStore_ListRunning(t *testing.T) {
	ctx := context.Background()
	clock := testutil.NewFakeClock(epoch)
	store := NewStateStore(state.NewMemoryStore(), clock)

	running, _ := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup, State: StateRunning})
	done, _ := store.Create(ctx, Job{SpaceID: "s1", Kind: KindBackup, State: StateRunning})
	if err := store.Finish(ctx, done.ID, Outcome{State: StateSucceeded}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	got, err := store.ListRunning(ctx, "s1")
	if err != nil {
		t.Fatalf("ListRunning: %v", err)
	}
	if len(got) != 1 || got[0].ID != running.ID {
		t.Fatalf("ListRunning: %+v", got)
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
