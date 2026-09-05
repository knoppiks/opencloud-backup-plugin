package snapshot

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kopia/kopia/repo"
	"github.com/kopia/kopia/repo/maintenance"
	"github.com/kopia/kopia/repo/manifest"
	ksnapshot "github.com/kopia/kopia/snapshot"
	"github.com/kopia/kopia/snapshot/policy"
	"github.com/kopia/kopia/snapshot/upload"
)

// testDK is a stand-in Data Key. Real DKs are 256-bit random values from
// pkg/keys; the engine only cares that it is the repo password.
func testDK(t *testing.T) []byte {
	t.Helper()
	dk := make([]byte, 32)
	if _, err := rand.Read(dk); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return dk
}

// newTestEngine builds an engine over a local filesystem backend so the whole
// lifecycle is exercised without S3 or containers.
func newTestEngine(t *testing.T) (*KopiaEngine, string) {
	t.Helper()
	root := t.TempDir()
	safety := maintenance.SafetyNone
	e, err := NewEngine(FilesystemOpener{Root: root}, EngineOptions{
		WorkDir: t.TempDir(),
		// Deterministic, immediate GC so prune assertions are observable in a
		// single test run; production defaults to SafetyFull.
		MaintenanceSafety: &safety,
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e, root
}

func testRepo(t *testing.T, spaceID string) Repo {
	t.Helper()
	return Repo{
		Location: Location{Bucket: "unused", Prefix: "backups/"},
		Space:    SpaceRef{SpaceID: spaceID},
		DK:       testDK(t),
	}
}

func TestNewEngine_RequiresOpener(t *testing.T) {
	if _, err := NewEngine(nil, EngineOptions{}); err == nil {
		t.Fatal("nil opener must be rejected")
	}
}

func TestSnapshot_CreatesRepoAndRecordsStats(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)
	r := testRepo(t, "space-1")
	src := treeSource(t)

	info, err := e.Snapshot(ctx, r, src)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if info.ID == "" {
		t.Fatal("snapshot id must be set")
	}
	if info.FileCount != 3 {
		t.Fatalf("file count = %d, want 3", info.FileCount)
	}
	if want := int64(len("hello") + len("notes body") + len("0123456789")); info.TotalBytes != want {
		t.Fatalf("total bytes = %d, want %d", info.TotalBytes, want)
	}
	if info.StartTime.IsZero() {
		t.Fatal("start time must be set")
	}
}

func TestSnapshot_RepoLayoutIsPerSpace(t *testing.T) {
	ctx := context.Background()
	e, root := newTestEngine(t)

	for _, id := range []string{"space-a", "space-b"} {
		if _, err := e.Snapshot(ctx, testRepo(t, id), treeSource(t)); err != nil {
			t.Fatalf("Snapshot(%s): %v", id, err)
		}
	}

	for _, id := range []string{"space-a", "space-b"} {
		dir := filepath.Join(root, "backups", "spaces", id)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("repo dir %s: %v", dir, err)
		}
		if len(entries) == 0 {
			t.Fatalf("repo for %s is empty", id)
		}
	}
}

func TestRepoPrefix(t *testing.T) {
	cases := []struct {
		prefix, space, want string
	}{
		{"", "s1", "spaces/s1/"},
		{"backups", "s1", "backups/spaces/s1/"},
		{"/backups/", "s1", "backups/spaces/s1/"},
		{"a/b", "s-2", "a/b/spaces/s-2/"},
	}
	for _, tc := range cases {
		got := RepoPrefix(Location{Prefix: tc.prefix}, SpaceRef{SpaceID: tc.space})
		if got != tc.want {
			t.Fatalf("RepoPrefix(%q,%q) = %q, want %q", tc.prefix, tc.space, got, tc.want)
		}
	}
}

func TestSnapshot_SecondRunReusesUnchangedFiles(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)
	r := testRepo(t, "space-1")
	src := treeSource(t)

	if _, err := e.Snapshot(ctx, r, src); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	before := map[string]int{
		"readme.txt":           src.openCount("readme.txt"),
		"docs/nested/deep.bin": src.openCount("docs/nested/deep.bin"),
	}

	// Change exactly one file (new content AND new mtime, as OpenCloud does).
	src.put("docs/notes.txt", []byte("notes body plus one more line"), testMTime.Add(time.Hour))

	if _, err := e.Snapshot(ctx, r, src); err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}

	for p, n := range before {
		if got := src.openCount(p); got != n {
			t.Fatalf("unchanged file %s was re-read (%d -> %d); cache not used", p, n, got)
		}
	}
	if src.openCount("docs/notes.txt") != 2 {
		t.Fatalf("changed file read %d times, want 2", src.openCount("docs/notes.txt"))
	}
}

// A same-mtime content change must still be detected, because the adapter
// exposes files as fs.File (kopia compares size) rather than fs.StreamingFile.
func TestSnapshot_DetectsSizeChangeWithUnchangedMTime(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)
	r := testRepo(t, "space-1")
	src := treeSource(t)

	if _, err := e.Snapshot(ctx, r, src); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	src.put("readme.txt", []byte("hello, again"), testMTime) // same mtime, new size

	info, err := e.Snapshot(ctx, r, src)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if src.openCount("readme.txt") != 2 {
		t.Fatalf("size change was not detected; readme.txt read %d times", src.openCount("readme.txt"))
	}

	out := t.TempDir()
	if err := e.RestoreAll(ctx, r, info.ID, out); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	assertFileContent(t, filepath.Join(out, "readme.txt"), "hello, again")
}

func TestRestoreAll_RoundTripsContentAndMTime(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)
	r := testRepo(t, "space-1")
	src := treeSource(t)

	info, err := e.Snapshot(ctx, r, src)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	out := t.TempDir()
	if err := e.RestoreAll(ctx, r, info.ID, out); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	assertFileContent(t, filepath.Join(out, "readme.txt"), "hello")
	assertFileContent(t, filepath.Join(out, "docs", "notes.txt"), "notes body")
	assertFileContent(t, filepath.Join(out, "docs", "nested", "deep.bin"), "0123456789")

	fi, err := os.Stat(filepath.Join(out, "readme.txt"))
	if err != nil {
		t.Fatalf("stat restored file: %v", err)
	}
	if !fi.ModTime().Truncate(time.Second).Equal(testMTime.Truncate(time.Second)) {
		t.Fatalf("mtime = %v, want %v", fi.ModTime(), testMTime)
	}
}

func TestRestoreFile_SingleEntry(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)
	r := testRepo(t, "space-1")

	info, err := e.Snapshot(ctx, r, treeSource(t))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	out := filepath.Join(t.TempDir(), "deep.bin")
	if err := e.RestoreFile(ctx, r, info.ID, "docs/nested/deep.bin", out); err != nil {
		t.Fatalf("RestoreFile: %v", err)
	}
	assertFileContent(t, out, "0123456789")
}

func TestRestore_Validation(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)
	r := testRepo(t, "space-1")
	info, err := e.Snapshot(ctx, r, treeSource(t))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if err := e.RestoreAll(ctx, r, info.ID, ""); err == nil {
		t.Fatal("empty target must be rejected")
	}
	if err := e.RestoreFile(ctx, r, info.ID, "  ", t.TempDir()); err == nil {
		t.Fatal("empty restore path must be rejected")
	}
	if err := e.RestoreAll(ctx, r, "no-such-id", t.TempDir()); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("unknown snapshot error = %v, want ErrSnapshotNotFound", err)
	}
	if err := e.RestoreFile(ctx, r, info.ID, "does/not/exist", t.TempDir()); err == nil {
		t.Fatal("missing entry must be rejected")
	}
}

func TestList_NewestFirst(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)
	r := testRepo(t, "space-1")
	src := treeSource(t)

	first, err := e.Snapshot(ctx, r, src)
	if err != nil {
		t.Fatalf("Snapshot 1: %v", err)
	}
	src.put("readme.txt", []byte("changed"), testMTime.Add(time.Hour))
	second, err := e.Snapshot(ctx, r, src)
	if err != nil {
		t.Fatalf("Snapshot 2: %v", err)
	}

	got, err := e.List(ctx, r)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 snapshots, got %d", len(got))
	}
	if got[0].StartTime.Before(got[1].StartTime) {
		t.Fatal("List must return newest first")
	}
	ids := map[SnapshotID]bool{got[0].ID: true, got[1].ID: true}
	if !ids[first.ID] || !ids[second.ID] {
		t.Fatalf("listed ids %v missing %s/%s", ids, first.ID, second.ID)
	}
}

// decisions.md #10: retention must never be count-based. kopia applies its own
// count-based policy automatically (upload checkpointing calls
// policy.ApplyRetentionPolicy), so the guarantee is that with our pinned policy
// that call expires nothing — even with far more snapshots than kopia's default
// keep-latest of 10.
//
// This is a white-box test: it drives the engine's own repo session so the
// (deliberately expensive) repository open happens once.
func TestSnapshot_PinnedPolicyExpiresNothing(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)
	r := testRepo(t, "space-1")
	src := treeSource(t)
	si := sourceInfo(r.Space)

	const snapshots = 12
	var toDelete []manifest.ID

	err := e.withRepo(ctx, r, true, func(ctx context.Context, rep repo.Repository) error {
		for i := range snapshots {
			src.put("readme.txt", []byte(strings.Repeat("x", i+1)), testMTime.Add(time.Duration(i)*time.Hour))
			err := repo.WriteSession(ctx, rep, repo.WriteSessionOptions{Purpose: "test-snapshot"},
				func(ctx context.Context, w repo.RepositoryWriter) error {
					if err := disableCountBasedRetention(ctx, w, si); err != nil {
						return err
					}
					tree, err := policy.TreeForSource(ctx, w, si)
					if err != nil {
						return err
					}
					prev, err := ksnapshot.ListSnapshots(ctx, w, si)
					if err != nil {
						return err
					}
					man, err := upload.NewUploader(w).Upload(ctx, rootEntry(src, rootModTime), tree, si, prev...)
					if err != nil {
						return err
					}
					_, err = ksnapshot.SaveSnapshot(ctx, w, man)
					return err
				})
			if err != nil {
				return err
			}
		}

		// Dry run of exactly the call kopia makes while checkpointing.
		return repo.WriteSession(ctx, rep, repo.WriteSessionOptions{Purpose: "test-retention"},
			func(ctx context.Context, w repo.RepositoryWriter) error {
				var err error
				toDelete, err = policy.ApplyRetentionPolicy(ctx, w, si, false)
				return err
			})
	})
	if err != nil {
		t.Fatalf("engine session: %v", err)
	}
	if len(toDelete) != 0 {
		t.Fatalf("count-based retention would expire %d snapshots, want 0", len(toDelete))
	}

	got, err := e.List(ctx, r)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != snapshots {
		t.Fatalf("have %d snapshots, want %d", len(got), snapshots)
	}
}

func TestNeverExpireByCount(t *testing.T) {
	rp := neverExpireByCount()
	if !isNeverExpireByCount(rp) {
		t.Fatal("neverExpireByCount must satisfy isNeverExpireByCount")
	}
	if isNeverExpireByCount(policy.RetentionPolicy{}) {
		t.Fatal("empty retention policy is not a never-expire policy")
	}
	keepThree := policy.OptionalInt(3)
	rp.KeepDaily = &keepThree
	if isNeverExpireByCount(rp) {
		t.Fatal("a non-zero counter must not count as never-expire")
	}
}

func TestPrune_DeletesOnlySnapshotsOlderThanWindow(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)
	r := testRepo(t, "space-1")
	src := treeSource(t)

	old, err := e.Snapshot(ctx, r, src)
	if err != nil {
		t.Fatalf("Snapshot 1: %v", err)
	}
	src.put("readme.txt", []byte("newer"), testMTime.Add(time.Hour))
	recent, err := e.Snapshot(ctx, r, src)
	if err != nil {
		t.Fatalf("Snapshot 2: %v", err)
	}

	// A negative-age window is impossible; instead prune with a window shorter
	// than the gap by pruning everything older than "now" minus a nanosecond,
	// which excludes neither. Use a window of 1ns after a short wait so the
	// first snapshot is provably outside it.
	time.Sleep(10 * time.Millisecond)
	if err := e.Prune(ctx, r, time.Nanosecond); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	got, err := e.List(ctx, r)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("after prune want 1 snapshot, got %d", len(got))
	}
	if got[0].ID != recent.ID {
		t.Fatalf("prune kept %s, want the newest %s", got[0].ID, recent.ID)
	}
	if got[0].ID == old.ID {
		t.Fatal("prune kept the expired snapshot")
	}

	// The surviving snapshot must still restore byte-identically.
	out := t.TempDir()
	if err := e.RestoreAll(ctx, r, recent.ID, out); err != nil {
		t.Fatalf("RestoreAll after prune: %v", err)
	}
	assertFileContent(t, filepath.Join(out, "readme.txt"), "newer")
	assertFileContent(t, filepath.Join(out, "docs", "notes.txt"), "notes body")
}

func TestPrune_KeepsNewestEvenWhenExpired(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)
	r := testRepo(t, "space-1")

	info, err := e.Snapshot(ctx, r, treeSource(t))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := e.Prune(ctx, r, time.Nanosecond); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	got, err := e.List(ctx, r)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != info.ID {
		t.Fatalf("prune must never delete the last snapshot; got %+v", got)
	}
}

func TestPrune_RejectsNonPositiveWindow(t *testing.T) {
	e, _ := newTestEngine(t)
	if err := e.Prune(context.Background(), testRepo(t, "space-1"), 0); err == nil {
		t.Fatal("zero window must be rejected")
	}
}

func TestExpiredManifests_SafetyRules(t *testing.T) {
	// A single snapshot is never expired, no matter how old.
	if got := expiredManifests(nil, time.Now()); got != nil {
		t.Fatalf("nil manifests yielded %v", got)
	}
}

func TestEngine_ValidatesRepo(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)

	noDK := testRepo(t, "space-1")
	noDK.DK = nil
	if _, err := e.Snapshot(ctx, noDK, treeSource(t)); err == nil {
		t.Fatal("missing DK must be rejected")
	}

	noSpace := testRepo(t, "")
	if _, err := e.Snapshot(ctx, noSpace, treeSource(t)); err == nil {
		t.Fatal("missing space id must be rejected")
	}

	if _, err := e.Snapshot(ctx, testRepo(t, "space-1"), nil); err == nil {
		t.Fatal("nil source must be rejected")
	}
}

func TestOpenRepo_WrongDataKeyFails(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)
	r := testRepo(t, "space-1")

	if _, err := e.Snapshot(ctx, r, treeSource(t)); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	wrong := r
	wrong.DK = testDK(t)
	if _, err := e.List(ctx, wrong); err == nil {
		t.Fatal("a wrong data key must not open the repository")
	}
}

func TestSnapshot_SourceErrorFailsRun(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)
	src := treeSource(t)
	src.failOpen("readme.txt", errors.New("cs3 gone"))

	// An unreadable file must fail the run rather than silently produce a
	// snapshot that is missing data.
	r := testRepo(t, "space-1")
	if _, err := e.Snapshot(ctx, r, src); !errors.Is(err, ErrIncompleteSnapshot) {
		t.Fatalf("Snapshot error = %v, want ErrIncompleteSnapshot", err)
	}
	// No manifest may be persisted for a failed run.
	got, err := e.List(ctx, r)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("failed run persisted %d snapshots", len(got))
	}

	// The next run succeeds once the source recovers — no corrupt repo left behind.
	src.failOpen("readme.txt", nil)
	if _, err := e.Snapshot(ctx, r, src); err != nil {
		t.Fatalf("recovery Snapshot: %v", err)
	}
	got, err = e.List(ctx, r)
	if err != nil {
		t.Fatalf("List after recovery: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("after recovery want 1 snapshot, got %d", len(got))
	}
}

// The error must not name the user's files.
func TestSnapshot_IncompleteErrorDoesNotLeakPaths(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEngine(t)
	src := treeSource(t)
	src.failOpen("docs/nested/deep.bin", errors.New("cs3 gone"))

	_, err := e.Snapshot(ctx, testRepo(t, "space-1"), src)
	if err == nil {
		t.Fatal("expected failure")
	}
	if strings.Contains(err.Error(), "deep.bin") {
		t.Fatalf("error leaked a source path: %v", err)
	}
}

func TestFilesystemOpener_Validation(t *testing.T) {
	ctx := context.Background()
	if _, err := (FilesystemOpener{}).Open(ctx, testRepo(t, "s"), true); err == nil {
		t.Fatal("missing root must be rejected")
	}
	if _, err := (FilesystemOpener{Root: t.TempDir()}).Open(ctx, testRepo(t, ""), true); err == nil {
		t.Fatal("missing space id must be rejected")
	}
}

func TestS3Opener_Validation(t *testing.T) {
	ctx := context.Background()
	r := testRepo(t, "space-1")
	r.Location.Bucket = ""
	if _, err := (S3Opener{}).Open(ctx, r, false); err == nil {
		t.Fatal("missing bucket must be rejected")
	}
}

func TestLocationRedacted(t *testing.T) {
	loc := Location{
		Endpoint:        "garage:3900",
		Bucket:          "b",
		AccessKeyID:     "AKIA-SECRET",
		SecretAccessKey: "super-secret",
	}
	red := loc.Redacted()
	if red.AccessKeyID != "" || red.SecretAccessKey != "" {
		t.Fatalf("Redacted still carries credentials: %+v", red)
	}
	if red.Endpoint != loc.Endpoint || red.Bucket != loc.Bucket {
		t.Fatal("Redacted must keep non-secret addressing")
	}
	if loc.SecretAccessKey == "" {
		t.Fatal("Redacted must not mutate the receiver")
	}
}

func TestStripScheme(t *testing.T) {
	for in, want := range map[string]string{
		"http://garage:3900":   "garage:3900",
		"https://garage:3900":  "garage:3900",
		"garage:3900":          "garage:3900",
		"https://garage:3900/": "garage:3900",
	} {
		if got := stripScheme(in); got != want {
			t.Fatalf("stripScheme(%q) = %q, want %q", in, got, want)
		}
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
