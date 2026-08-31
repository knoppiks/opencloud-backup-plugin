package spacecfg

import (
	"context"
	"errors"
	"testing"
	"time"

	"opencloud-backup-plugin/internal/testutil"
)

var epoch = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func newStore(t *testing.T) (*MemoryStore, *testutil.FakeClock) {
	t.Helper()
	clock := testutil.NewFakeClock(epoch)
	return NewMemoryStoreWithClock(clock), clock
}

func TestEffectiveRetentionWindow(t *testing.T) {
	if got := (Config{}).EffectiveRetentionWindow(); got != DefaultRetentionWindow {
		t.Fatalf("zero window = %v, want default %v", got, DefaultRetentionWindow)
	}
	custom := 30 * 24 * time.Hour
	if got := (Config{RetentionWindow: custom}).EffectiveRetentionWindow(); got != custom {
		t.Fatalf("custom window = %v, want %v", got, custom)
	}
}

// Retention must be a duration, never a count (decisions.md #10). The default
// is deliberately deep: depth is what defeats slow-burn ransomware.
func TestDefaultRetentionWindowIsDeep(t *testing.T) {
	if DefaultRetentionWindow < 30*24*time.Hour {
		t.Fatalf("default retention window %v is too shallow", DefaultRetentionWindow)
	}
}

func TestPut_SetsTimestampsAndPreservesCreatedAt(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)

	created, err := store.Put(ctx, Config{SpaceID: "s1", TargetID: "t1", Enabled: true})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !created.CreatedAt.Equal(epoch) || !created.UpdatedAt.Equal(epoch) {
		t.Fatalf("timestamps = %+v", created)
	}

	clock.Advance(time.Hour)
	updated, err := store.Put(ctx, Config{SpaceID: "s1", TargetID: "t2"})
	if err != nil {
		t.Fatalf("Put update: %v", err)
	}
	if !updated.CreatedAt.Equal(epoch) {
		t.Fatalf("CreatedAt changed on update: %v", updated.CreatedAt)
	}
	if !updated.UpdatedAt.Equal(epoch.Add(time.Hour)) {
		t.Fatalf("UpdatedAt = %v", updated.UpdatedAt)
	}
	if updated.TargetID != "t2" {
		t.Fatalf("target not replaced: %q", updated.TargetID)
	}
}

func TestPut_Validation(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	cases := map[string]Config{
		"missing space id":  {TargetID: "t1"},
		"missing target id": {SpaceID: "s1"},
		"negative window":   {SpaceID: "s1", TargetID: "t1", RetentionWindow: -time.Second},
	}
	for name, c := range cases {
		if _, err := store.Put(ctx, c); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}

func TestGetDelete_NotFound(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	_, err := store.Get(ctx, "absent")
	var nf ErrNotFound
	if !errors.As(err, &nf) || nf.SpaceID != "absent" {
		t.Fatalf("Get error = %v, want ErrNotFound{absent}", err)
	}
	if err := store.Delete(ctx, "absent"); !errors.As(err, &nf) {
		t.Fatalf("Delete error = %v, want ErrNotFound", err)
	}
}

func TestRoundTripAndDelete(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	want := Config{SpaceID: "s1", TargetID: "t1", RetentionWindow: 7 * 24 * time.Hour, Enabled: true}
	if _, err := store.Put(ctx, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.Get(ctx, "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TargetID != want.TargetID || got.RetentionWindow != want.RetentionWindow || !got.Enabled {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}

	if err := store.Delete(ctx, "s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(ctx, "s1"); err == nil {
		t.Fatal("config still present after delete")
	}
}

func TestList_SortedBySpaceID(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	for _, id := range []string{"s3", "s1", "s2"} {
		if _, err := store.Put(ctx, Config{SpaceID: id, TargetID: "t"}); err != nil {
			t.Fatalf("Put %s: %v", id, err)
		}
	}
	got, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 || got[0].SpaceID != "s1" || got[1].SpaceID != "s2" || got[2].SpaceID != "s3" {
		t.Fatalf("List = %+v, want sorted by space id", got)
	}
}

func TestMemoryStore_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			_, _ = store.Put(ctx, Config{SpaceID: "s1", TargetID: "t1"})
		}
	}()
	for range 100 {
		_, _ = store.Get(ctx, "s1")
		_, _ = store.List(ctx)
	}
	<-done
}
