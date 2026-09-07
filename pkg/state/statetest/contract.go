// Package statetest holds the behavioural contract every state.Store must
// satisfy, so a second implementation cannot quietly differ from the one the
// rest of the test suite exercises.
package statetest

import (
	"context"
	"testing"

	"opencloud-backup-plugin/pkg/state"
)

// RunStoreContract exercises the promises state.Store makes. newStore must
// return an empty store.
func RunStoreContract(t *testing.T, newStore func(t *testing.T) state.Store) {
	t.Helper()

	t.Run("missing keys report not-found", func(t *testing.T) {
		ctx := context.Background()
		st := newStore(t)

		if _, err := st.Get(ctx, "absent/key"); !state.IsNotFound(err) {
			t.Fatalf("Get missing = %v, want not-found", err)
		}
		if err := st.Delete(ctx, "absent/key"); !state.IsNotFound(err) {
			t.Fatalf("Delete missing = %v, want not-found", err)
		}
	})

	t.Run("values round trip", func(t *testing.T) {
		ctx := context.Background()
		st := newStore(t)

		if err := st.Create(ctx, "docs/one", []byte(`{"a":1}`)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := st.Get(ctx, "docs/one")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if string(got) != `{"a":1}` {
			t.Fatalf("Get = %q", got)
		}
	})

	// The property the append-only records depend on: a create can never be the
	// thing that destroys the previous value.
	t.Run("create refuses to replace and leaves the stored value alone", func(t *testing.T) {
		ctx := context.Background()
		st := newStore(t)

		if err := st.Create(ctx, "docs/one", []byte("first")); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := st.Create(ctx, "docs/one", []byte("second")); !state.IsExists(err) {
			t.Fatalf("Create again = %v, want already-exists", err)
		}
		got, err := st.Get(ctx, "docs/one")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if string(got) != "first" {
			t.Fatalf("Get = %q, want the original", got)
		}
	})

	t.Run("replace overwrites, and creates when absent", func(t *testing.T) {
		ctx := context.Background()
		st := newStore(t)

		if err := st.Replace(ctx, "docs/one", []byte("first")); err != nil {
			t.Fatalf("Replace of a missing key: %v", err)
		}
		if err := st.Replace(ctx, "docs/one", []byte("second")); err != nil {
			t.Fatalf("Replace: %v", err)
		}
		got, err := st.Get(ctx, "docs/one")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if string(got) != "second" {
			t.Fatalf("Get = %q, want the replacement", got)
		}
	})

	t.Run("delete removes", func(t *testing.T) {
		ctx := context.Background()
		st := newStore(t)

		if err := st.Create(ctx, "docs/one", []byte("value")); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := st.Delete(ctx, "docs/one"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := st.Get(ctx, "docs/one"); !state.IsNotFound(err) {
			t.Fatalf("Get after delete = %v", err)
		}
	})

	t.Run("list is recursive, sorted and prefix-scoped", func(t *testing.T) {
		ctx := context.Background()
		st := newStore(t)

		keys := []string{
			"jobs/space/2",
			"jobs/space/1",
			"jobs/spaceother/1",
			"leases/space",
		}
		for _, k := range keys {
			if err := st.Create(ctx, k, []byte("{}")); err != nil {
				t.Fatalf("Create %s: %v", k, err)
			}
		}

		got, err := st.List(ctx, "jobs/space")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		want := []string{"jobs/space/1", "jobs/space/2"}
		assertKeys(t, got, want)

		all, err := st.List(ctx, "jobs")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		assertKeys(t, all, []string{"jobs/space/1", "jobs/space/2", "jobs/spaceother/1"})
	})

	t.Run("listing an unwritten prefix is empty, not an error", func(t *testing.T) {
		ctx := context.Background()
		st := newStore(t)

		got, err := st.List(ctx, "nothing/here")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("List = %v, want empty", got)
		}
	})
}

func assertKeys(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("keys = %v, want %v", got, want)
		}
	}
}
