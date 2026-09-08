package state_test

import (
	"context"
	"errors"
	"testing"

	"opencloud-backup-plugin/pkg/state"
	"opencloud-backup-plugin/pkg/state/statetest"
)

type doc struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestEscapeSegmentRoundTrips(t *testing.T) {
	t.Parallel()

	cases := []string{
		"simple",
		// The shape OpenCloud space ids actually take.
		"1284d238-aa92-42ce-bdc4-0b0000009157$4c510ada!4c510ada",
		"with/slash",
		"with space",
		".",
		"..",
		"",
		"ümlaut",
	}
	for _, in := range cases {
		esc := state.EscapeSegment(in)
		for _, bad := range []string{"/", " "} {
			if contains(esc, bad) {
				t.Fatalf("escaped %q still contains %q: %q", in, bad, esc)
			}
		}
		if esc == "." || esc == ".." {
			t.Fatalf("escaped %q is a path traversal segment", in)
		}
		back, err := state.UnescapeSegment(esc)
		if err != nil {
			t.Fatalf("unescape %q: %v", esc, err)
		}
		if back != in {
			t.Fatalf("round trip: got %q, want %q", back, in)
		}
	}
}

func TestKeyJoinsEscapedSegments(t *testing.T) {
	t.Parallel()

	got := state.Key("jobs", "a/b", "c")
	want := "jobs/a%2Fb/c"
	if got != want {
		t.Fatalf("Key: got %q, want %q", got, want)
	}
}

func TestMemoryStoreContract(t *testing.T) {
	statetest.RunStoreContract(t, func(*testing.T) state.Store { return state.NewMemoryStore() })
}

func TestMemoryStoreRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := state.NewMemoryStore()

	if _, err := st.Get(ctx, "missing"); !state.IsNotFound(err) {
		t.Fatalf("Get missing: got %v, want not-found", err)
	}
	if err := st.Delete(ctx, "missing"); !state.IsNotFound(err) {
		t.Fatalf("Delete missing: got %v, want not-found", err)
	}

	if err := st.Create(ctx, "a/b", []byte("value")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := st.Get(ctx, "a/b")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "value" {
		t.Fatalf("Get: got %q", got)
	}

	// The Store must hand out copies, not aliases of its own buffers.
	got[0] = 'X'
	again, err := st.Get(ctx, "a/b")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(again) != "value" {
		t.Fatalf("stored value was mutated through the returned slice: %q", again)
	}

	if err := st.Delete(ctx, "a/b"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.Get(ctx, "a/b"); !state.IsNotFound(err) {
		t.Fatalf("Get after delete: got %v", err)
	}
}

func TestMemoryStoreListMatchesWholeSegments(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := state.NewMemoryStore()
	for _, k := range []string{"jobs/space/1", "jobs/space/2", "jobs/spaceother/1", "other/1"} {
		if err := st.Create(ctx, k, []byte("{}")); err != nil {
			t.Fatalf("Create %s: %v", k, err)
		}
	}

	got, err := st.List(ctx, "jobs/space")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"jobs/space/1", "jobs/space/2"}
	if len(got) != len(want) {
		t.Fatalf("List: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDocumentsRoundTripAndList(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	docs := state.NewDocuments[doc](state.NewMemoryStore(), "docs")

	if err := docs.Create(ctx, doc{Name: "one", Count: 1}, "space$a!a", "one"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := docs.Create(ctx, doc{Name: "two", Count: 2}, "space$a!a", "two"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := docs.Create(ctx, doc{Name: "elsewhere"}, "other", "one"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := docs.Get(ctx, "space$a!a", "one")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "one" || got.Count != 1 {
		t.Fatalf("Get: got %+v", got)
	}

	all, unreadable, err := docs.All(ctx, "space$a!a")
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(unreadable) != 0 {
		t.Fatalf("All reported %v as unreadable", unreadable)
	}
	if len(all) != 2 || all[0].Name != "one" || all[1].Name != "two" {
		t.Fatalf("All: got %+v", all)
	}

	if err := docs.Delete(ctx, "space$a!a", "one"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := docs.Get(ctx, "space$a!a", "one"); !state.IsNotFound(err) {
		t.Fatalf("Get after delete: %v", err)
	}
}

func TestDocumentsSkipsMalformedRecords(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := state.NewMemoryStore()
	docs := state.NewDocuments[doc](st, "docs")

	if err := docs.Create(ctx, doc{Name: "good"}, "space", "good"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := st.Create(ctx, "docs/space/bad", []byte("{not json")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// One corrupt record must not make a Space's whole history unreadable —
	// and must not disappear without trace either.
	all, unreadable, err := docs.All(ctx, "space")
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 1 || all[0].Name != "good" {
		t.Fatalf("All: got %+v", all)
	}
	if len(unreadable) != 1 || unreadable[0] != "docs/space/bad" {
		t.Fatalf("All reported %v as unreadable, want the corrupt key", unreadable)
	}

	if _, err := docs.Get(ctx, "space", "bad"); err == nil {
		t.Fatal("Get of a malformed document: want error")
	} else if state.IsNotFound(err) {
		t.Fatalf("malformed document reported as missing: %v", err)
	}
}

func TestErrNotFoundIsMatchable(t *testing.T) {
	t.Parallel()

	err := error(state.ErrNotFound{Key: "k"})
	var target state.ErrNotFound
	if !errors.As(err, &target) || target.Key != "k" {
		t.Fatalf("errors.As: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
