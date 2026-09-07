package state_test

import (
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/state"
)

var versionEpoch = time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)

func TestVersionsAppendNeverReplaces(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	backing := state.NewMemoryStore()
	versions := state.NewVersions[doc](backing, "records")

	if err := versions.Append(ctx, versionEpoch, doc{Name: "first"}, "space$a!a"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := versions.Append(ctx, versionEpoch.Add(time.Minute), doc{Name: "second"}, "space$a!a"); err != nil {
		t.Fatalf("Append: %v", err)
	}

	keys, err := backing.List(ctx, "records")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("keys = %v, want two versions", keys)
	}

	got, err := versions.Newest(ctx, "space$a!a")
	if err != nil {
		t.Fatalf("Newest: %v", err)
	}
	if got.Name != "second" {
		t.Fatalf("Newest = %+v, want the later version", got)
	}
}

// A stopped clock (every test has one) and a clock that jumps backwards (every
// server has one eventually) must not be able to collide with, or sort before,
// the version they supersede.
func TestVersionsAppendIsMonotonic(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	versions := state.NewVersions[doc](state.NewMemoryStore(), "records")

	for _, at := range []time.Time{versionEpoch, versionEpoch, versionEpoch.Add(-time.Hour)} {
		if err := versions.Append(ctx, at, doc{Name: at.String()}, "s"); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	keys, err := versions.Keys(ctx, "s")
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("keys = %v, want three versions", keys)
	}

	got, err := versions.Newest(ctx, "s")
	if err != nil {
		t.Fatalf("Newest: %v", err)
	}
	if got.Name != versionEpoch.Add(-time.Hour).String() {
		t.Fatalf("Newest = %+v, want the last write", got)
	}
}

func TestVersionsLoadReportsTheSpanOfStoredVersions(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	versions := state.NewVersions[doc](state.NewMemoryStore(), "records")

	if err := versions.Append(ctx, versionEpoch, doc{Name: "first"}, "s"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := versions.Append(ctx, versionEpoch.Add(time.Hour), doc{Name: "second"}, "s"); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, span, err := versions.Load(ctx, "s")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Name != "second" {
		t.Fatalf("Load = %+v", got)
	}
	if !span.Oldest.Equal(versionEpoch) || !span.Newest.Equal(versionEpoch.Add(time.Hour)) {
		t.Fatalf("span = %+v", span)
	}
}

func TestVersionsMissingRecordIsNotFound(t *testing.T) {
	t.Parallel()

	versions := state.NewVersions[doc](state.NewMemoryStore(), "records")
	if _, err := versions.Newest(t.Context(), "absent"); !state.IsNotFound(err) {
		t.Fatalf("Newest = %v, want not-found", err)
	}
}

func TestVersionsLatestReturnsTheNewestOfEachRecord(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	versions := state.NewVersions[doc](state.NewMemoryStore(), "records")

	for _, v := range []struct {
		id   string
		at   time.Time
		name string
	}{
		{"b", versionEpoch, "b-old"},
		{"b", versionEpoch.Add(time.Hour), "b-new"},
		{"a", versionEpoch, "a-only"},
	} {
		if err := versions.Append(ctx, v.at, doc{Name: v.name}, v.id); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	all, err := versions.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(all) != 2 || all[0].Name != "a-only" || all[1].Name != "b-new" {
		t.Fatalf("Latest = %+v", all)
	}
}

// The pre-versioned layout stays readable, is superseded by a version, and is
// never rewritten by an append.
func TestVersionsFallsBackToTheLegacyLayout(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	backing := state.NewMemoryStore()
	legacy := state.NewDocuments[doc](backing, "old")
	if err := legacy.Create(ctx, doc{Name: "legacy"}, "s"); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	if err := legacy.Create(ctx, doc{Name: "legacy-other"}, "other"); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	versions := state.NewVersions[doc](backing, "records").WithLegacy("old")

	got, err := versions.Newest(ctx, "s")
	if err != nil || got.Name != "legacy" {
		t.Fatalf("Newest = %+v (%v), want the legacy record", got, err)
	}

	if err := versions.Append(ctx, versionEpoch, doc{Name: "versioned"}, "s"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err = versions.Newest(ctx, "s")
	if err != nil || got.Name != "versioned" {
		t.Fatalf("Newest = %+v (%v), want the version", got, err)
	}
	if stored, err := legacy.Get(ctx, "s"); err != nil || stored.Name != "legacy" {
		t.Fatalf("legacy record = %+v (%v), want it untouched", stored, err)
	}

	// Listing sees each record once: versioned where a version exists, legacy
	// where it does not.
	all, err := versions.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Latest = %+v, want one entry per record", all)
	}
	names := map[string]bool{}
	for _, d := range all {
		names[d.Name] = true
	}
	if !names["versioned"] || !names["legacy-other"] {
		t.Fatalf("Latest = %+v", all)
	}
}

func TestVersionsDeleteAllRemovesEveryVersionAndTheLegacyRecord(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	backing := state.NewMemoryStore()
	legacy := state.NewDocuments[doc](backing, "old")
	if err := legacy.Create(ctx, doc{Name: "legacy"}, "s"); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	versions := state.NewVersions[doc](backing, "records").WithLegacy("old")
	if err := versions.Append(ctx, versionEpoch, doc{Name: "one"}, "s"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := versions.Append(ctx, versionEpoch.Add(time.Hour), doc{Name: "two"}, "s"); err != nil {
		t.Fatalf("Append: %v", err)
	}

	removed, err := versions.DeleteAll(ctx, "s")
	if err != nil || !removed {
		t.Fatalf("DeleteAll = %v (%v)", removed, err)
	}
	if _, err := versions.Newest(ctx, "s"); !state.IsNotFound(err) {
		t.Fatalf("Newest after delete = %v", err)
	}

	removed, err = versions.DeleteAll(ctx, "s")
	if err != nil {
		t.Fatalf("DeleteAll again: %v", err)
	}
	if removed {
		t.Fatal("DeleteAll reported a removal for a record that was already gone")
	}
}

func TestVersionsIDsListsEveryRecordOnce(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	backing := state.NewMemoryStore()
	legacy := state.NewDocuments[doc](backing, "old")
	if err := legacy.Create(ctx, doc{Name: "legacy"}, "space$only!legacy"); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	if err := legacy.Create(ctx, doc{Name: "both"}, "space$both!both"); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	versions := state.NewVersions[doc](backing, "records").WithLegacy("old")
	// A record filed under a further segment (as key envelopes are, one per
	// kind) still reports the id it is indexed by.
	if err := versions.Append(ctx, versionEpoch, doc{Name: "rk"}, "space$both!both", "rk"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := versions.Append(ctx, versionEpoch, doc{Name: "srw"}, "space$both!both", "srw"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := versions.Append(ctx, versionEpoch, doc{Name: "rk"}, "space$new!new", "rk"); err != nil {
		t.Fatalf("Append: %v", err)
	}

	ids, err := versions.IDs(ctx)
	if err != nil {
		t.Fatalf("IDs: %v", err)
	}
	want := []string{"space$both!both", "space$new!new", "space$only!legacy"}
	if len(ids) != len(want) {
		t.Fatalf("IDs = %v, want %v", ids, want)
	}
	for i, id := range want {
		if ids[i] != id {
			t.Fatalf("IDs = %v, want %v", ids, want)
		}
	}
}

func TestVersionsIDsOnAnEmptyCollection(t *testing.T) {
	t.Parallel()

	versions := state.NewVersions[doc](state.NewMemoryStore(), "records")
	ids, err := versions.IDs(t.Context())
	if err != nil {
		t.Fatalf("IDs: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("IDs = %v, want none", ids)
	}
}

func TestNewestKeyPicksTheLastVersion(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	backing := state.NewMemoryStore()
	versions := state.NewVersions[doc](backing, "records")
	if err := versions.Append(ctx, versionEpoch, doc{Name: "first"}, "s"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := versions.Append(ctx, versionEpoch.Add(time.Hour), doc{Name: "second"}, "s"); err != nil {
		t.Fatalf("Append: %v", err)
	}

	key, err := state.NewestKey(ctx, backing, "records/s")
	if err != nil {
		t.Fatalf("NewestKey: %v", err)
	}
	got, err := backing.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != `{"name":"second","count":0}` {
		t.Fatalf("newest document = %s", got)
	}

	if _, err := state.NewestKey(ctx, backing, "records/absent"); !state.IsNotFound(err) {
		t.Fatalf("NewestKey of an empty prefix = %v, want not-found", err)
	}
}
