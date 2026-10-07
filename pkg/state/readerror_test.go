package state_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/state"
)

// flakyStore fails reads of chosen keys with a chosen error, on top of a real
// store. It stands for a backend that blinks or refuses one document.
type flakyStore struct {
	state.Store
	failGet map[string]error
}

func (f flakyStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err, ok := f.failGet[key]; ok {
		return nil, err
	}
	return f.Store.Get(ctx, key)
}

var errTransient = errors.New("backend unavailable")

// listed returns the single key below prefix, so a test can make exactly that
// document fail.
func listed(t *testing.T, st state.Store, prefix string) string {
	t.Helper()
	keys, err := st.List(t.Context(), prefix)
	if err != nil || len(keys) != 1 {
		t.Fatalf("List(%q) = %v (%v), want one key", prefix, keys, err)
	}
	return keys[0]
}

// A record that exists but could not be read must not drop out of the
// listing: for a Space configuration that is a Space that silently stops being
// backed up (review-2026-10.md F2). The listing fails instead.
func TestVersionsLatestFailsOnATransientReadError(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	backing := state.NewMemoryStore()
	writer := state.NewVersions[doc](backing, "records")
	for _, id := range []string{"a", "b"} {
		if err := writer.Append(ctx, time.Unix(1, 0), doc{Name: id}, id); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	key := listed(t, backing, "records/b")

	flaky := flakyStore{Store: backing, failGet: map[string]error{key: errTransient}}
	got, unreadable, err := state.NewVersions[doc](flaky, "records").Latest(ctx)
	if !errors.Is(err, errTransient) {
		t.Fatalf("Latest = %+v, %v, %v; want the transient error", got, unreadable, err)
	}
}

func TestVersionsLatestFailsOnATransientLegacyReadError(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	backing := state.NewMemoryStore()
	if err := state.NewDocuments[doc](backing, "old").Create(ctx, doc{Name: "legacy"}, "s"); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	flaky := flakyStore{Store: backing, failGet: map[string]error{"old/s": errTransient}}
	_, _, err := state.NewVersions[doc](flaky, "records").WithLegacy("old").Latest(ctx)
	if !errors.Is(err, errTransient) {
		t.Fatalf("Latest = %v, want the transient error", err)
	}
}

// Not found between listing and reading is a concurrent delete: skipped
// silently, the rest still listed.
func TestVersionsLatestSkipsARecordDeletedUnderneathIt(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	backing := state.NewMemoryStore()
	writer := state.NewVersions[doc](backing, "records")
	for _, id := range []string{"a", "b"} {
		if err := writer.Append(ctx, time.Unix(1, 0), doc{Name: id}, id); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	key := listed(t, backing, "records/b")

	flaky := flakyStore{Store: backing, failGet: map[string]error{key: state.ErrNotFound{Key: key}}}
	got, unreadable, err := state.NewVersions[doc](flaky, "records").Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(got) != 1 || got[0].Name != "a" || len(unreadable) != 0 {
		t.Fatalf("Latest = %+v, unreadable %v; want only a", got, unreadable)
	}
}

func TestDocumentsAllFailsOnATransientReadError(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	backing := state.NewMemoryStore()
	if err := state.NewDocuments[doc](backing, "docs").Create(ctx, doc{Name: "x"}, "space", "x"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	flaky := flakyStore{Store: backing, failGet: map[string]error{"docs/space/x": errTransient}}
	if _, _, err := state.NewDocuments[doc](flaky, "docs").All(ctx, "space"); !errors.Is(err, errTransient) {
		t.Fatalf("All = %v, want the transient error", err)
	}
}
