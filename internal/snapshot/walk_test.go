package snapshot

// Tests for the streaming restore surface (Phase 5): Walk, DirOpener, and the
// envelope object layout.

import (
	"context"
	"errors"
	"io"
	"sort"
	"testing"
)

// walkAll collects every entry a Walk yields.
func walkAll(t *testing.T, e *KopiaEngine, r Repo, id SnapshotID) []RestoredEntry {
	t.Helper()
	var got []RestoredEntry
	err := e.Walk(context.Background(), r, id, func(_ context.Context, entry RestoredEntry) error {
		got = append(got, entry)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	return got
}

func TestWalk_YieldsWholeTreeWithContentAndMetadata(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)
	r := testRepo(t, "space-1")

	info, err := e.Snapshot(ctx, r, treeSource(t))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	entries := walkAll(t, e, r, info.ID)

	paths := make([]string, 0, len(entries))
	byPath := map[string]RestoredEntry{}
	for _, entry := range entries {
		paths = append(paths, entry.Path)
		byPath[entry.Path] = entry
	}
	sort.Strings(paths)

	want := []string{"docs", "docs/nested", "docs/nested/deep.bin", "docs/notes.txt", "readme.txt"}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %v, want %v", paths, want)
		}
	}

	if dir := byPath["docs"]; !dir.IsDir || dir.Open != nil {
		t.Fatalf("docs = %+v, want a directory with no reader", dir)
	}

	file := byPath["docs/nested/deep.bin"]
	if file.IsDir {
		t.Fatal("deep.bin reported as a directory")
	}
	if file.Size != int64(len("0123456789")) {
		t.Fatalf("size = %d, want %d", file.Size, len("0123456789"))
	}
	if !file.ModTime.Truncate(0).Equal(testMTime.Truncate(0)) {
		t.Fatalf("mtime = %v, want %v", file.ModTime, testMTime)
	}

	rc, err := file.Open(ctx)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != "0123456789" {
		t.Fatalf("content = %q", body)
	}
}

// A directory must be announced before anything inside it, so a consumer can
// create the container before writing into it (Path B uploads into CS3).
func TestWalk_ReportsDirectoryBeforeChildren(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)
	r := testRepo(t, "space-1")

	info, err := e.Snapshot(ctx, r, treeSource(t))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	seen := map[string]int{}
	for i, entry := range walkAll(t, e, r, info.ID) {
		seen[entry.Path] = i
	}
	if seen["docs"] > seen["docs/notes.txt"] || seen["docs/nested"] > seen["docs/nested/deep.bin"] {
		t.Fatalf("children appeared before their directory: %v", seen)
	}
}

func TestWalk_CallbackErrorAborts(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)
	r := testRepo(t, "space-1")

	info, err := e.Snapshot(ctx, r, treeSource(t))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	sentinel := errors.New("stop")
	calls := 0
	err = e.Walk(ctx, r, info.ID, func(context.Context, RestoredEntry) error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the callback error", err)
	}
	if calls != 1 {
		t.Fatalf("callback called %d times after failing, want 1", calls)
	}
}

func TestWalk_Validation(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)
	r := testRepo(t, "space-1")

	info, err := e.Snapshot(ctx, r, treeSource(t))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if err := e.Walk(ctx, r, info.ID, nil); err == nil {
		t.Fatal("a nil callback must be rejected")
	}
	err = e.Walk(ctx, r, "no-such-snapshot", func(context.Context, RestoredEntry) error { return nil })
	if !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("err = %v, want ErrSnapshotNotFound", err)
	}
}

// The offline decrypt path opens a repository that was copied out of S3 into a
// plain directory: no prefix arithmetic, no space id in the path.
func TestDirOpener_OpensRepositoryCopiedOutOfTheTarget(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	e, err := NewEngine(FilesystemOpener{Root: root}, EngineOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	r := testRepo(t, "space-1")
	info, err := e.Snapshot(ctx, r, treeSource(t))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// The repository directory as a Take-Out would have copied it out.
	repoDir := root + "/" + RepoPrefix(r.Location, r.Space)

	offline, err := NewEngine(DirOpener{Dir: repoDir}, EngineOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewEngine(DirOpener): %v", err)
	}
	list, err := offline.List(ctx, r)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != info.ID {
		t.Fatalf("list = %+v, want the single snapshot %s", list, info.ID)
	}

	out := t.TempDir()
	if err := offline.RestoreAll(ctx, r, info.ID, out); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	assertFileContent(t, out+"/readme.txt", "hello")
}

func TestDirOpener_RequiresDir(t *testing.T) {
	if _, err := (DirOpener{}).Open(context.Background(), Repo{}, false); err == nil {
		t.Fatal("an empty directory must be rejected")
	}
}

func TestEnvelopeKey(t *testing.T) {
	cases := []struct {
		prefix, space, want string
	}{
		{"", "s1", "keys/s1/recovery.ocbke"},
		{"backups", "s1", "backups/keys/s1/recovery.ocbke"},
		{"/backups/", "s1", "backups/keys/s1/recovery.ocbke"},
		{"a/b", "storage$space-2", "a/b/keys/storage$space-2/recovery.ocbke"},
	}
	for _, tc := range cases {
		got := EnvelopeKey(Location{Prefix: tc.prefix}, SpaceRef{SpaceID: tc.space})
		if got != tc.want {
			t.Fatalf("EnvelopeKey(%q,%q) = %q, want %q", tc.prefix, tc.space, got, tc.want)
		}
	}
}

// The envelope must never land under a repository prefix: everything there is
// kopia-owned and maintenance may reclaim what it does not recognise.
func TestEnvelopeKey_IsOutsideTheRepoPrefix(t *testing.T) {
	loc := Location{Prefix: "backups/"}
	ref := SpaceRef{SpaceID: "space-1"}

	key := EnvelopeKey(loc, ref)
	if repoPrefix := RepoPrefix(loc, ref); len(key) >= len(repoPrefix) && key[:len(repoPrefix)] == repoPrefix {
		t.Fatalf("envelope key %q lives inside the kopia repo prefix %q", key, repoPrefix)
	}
}
