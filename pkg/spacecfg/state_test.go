package spacecfg

import (
	"context"
	"errors"
	"testing"
	"time"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/state"
)

func TestEffectiveSchedule(t *testing.T) {
	if got := (Config{}).EffectiveSchedule(); got != DefaultSchedule {
		t.Fatalf("empty schedule = %q, want default %q", got, DefaultSchedule)
	}
	if got := (Config{Schedule: "0 4 * * 0"}).EffectiveSchedule(); got != "0 4 * * 0" {
		t.Fatalf("custom schedule = %q", got)
	}
}

// Both Store implementations must behave identically; the memory one is what
// most tests inject, the state one is what actually runs.
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

			var nf ErrNotFound
			if _, err := store.Get(ctx, "absent"); !errors.As(err, &nf) {
				t.Fatalf("Get absent: %v", err)
			}
			if err := store.Delete(ctx, "absent"); !errors.As(err, &nf) {
				t.Fatalf("Delete absent: %v", err)
			}
			if _, err := store.Put(ctx, Config{TargetID: "t1"}); err == nil {
				t.Fatal("missing space id must be rejected")
			}
			if _, err := store.Put(ctx, Config{SpaceID: "s1"}); err == nil {
				t.Fatal("missing target id must be rejected")
			}
			if _, err := store.Put(ctx, Config{SpaceID: "s1", TargetID: "t1", RetentionWindow: -time.Hour}); err == nil {
				t.Fatal("negative retention must be rejected")
			}

			stored, err := store.Put(ctx, Config{
				SpaceID:  "space$a!a",
				TargetID: "t1",
				Schedule: "0 3 * * *",
				Enabled:  true,
			})
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			if !stored.CreatedAt.Equal(epoch) || !stored.UpdatedAt.Equal(epoch) {
				t.Fatalf("timestamps = %+v", stored)
			}

			clock.Advance(time.Hour)
			updated, err := store.Put(ctx, Config{SpaceID: "space$a!a", TargetID: "t2", Enabled: false})
			if err != nil {
				t.Fatalf("Put update: %v", err)
			}
			if !updated.CreatedAt.Equal(epoch) {
				t.Fatalf("CreatedAt must be preserved: %v", updated.CreatedAt)
			}
			if !updated.UpdatedAt.Equal(epoch.Add(time.Hour)) {
				t.Fatalf("UpdatedAt = %v", updated.UpdatedAt)
			}

			got, err := store.Get(ctx, "space$a!a")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.TargetID != "t2" || got.Enabled {
				t.Fatalf("stored config = %+v", got)
			}

			if _, err := store.Put(ctx, Config{SpaceID: "b-space", TargetID: "t1"}); err != nil {
				t.Fatalf("Put second: %v", err)
			}
			list, err := store.List(ctx)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(list) != 2 {
				t.Fatalf("List = %+v", list)
			}

			if err := store.Delete(ctx, "space$a!a"); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if _, err := store.Get(ctx, "space$a!a"); !errors.As(err, &nf) {
				t.Fatalf("Get after delete: %v", err)
			}
		})
	}
}

func TestStateStore_SurvivesRestart(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)

	before := NewStateStore(backing, clock)
	if _, err := before.Put(ctx, Config{
		SpaceID:         "s1",
		TargetID:        "t1",
		Schedule:        "15 1 * * 1",
		RetentionWindow: 30 * 24 * time.Hour,
		Enabled:         true,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// A schedule that dies with the process is not a schedule.
	after := NewStateStore(backing, clock)
	got, err := after.Get(ctx, "s1")
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if got.Schedule != "15 1 * * 1" || !got.Enabled || got.TargetID != "t1" {
		t.Fatalf("config after restart: %+v", got)
	}
	if got.EffectiveRetentionWindow() != 30*24*time.Hour {
		t.Fatalf("retention after restart: %v", got.EffectiveRetentionWindow())
	}
}
