package backup

// End-to-end pipeline test with the real kopia engine, but a local filesystem
// blob backend and a fake CS3 reader: CS3 read -> kopia snapshot/encrypt/dedup
// -> store -> restore. The Garage-backed variant lives behind the `integration`
// build tag; this one runs in every `go test` invocation.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/targets"
)

type pipeline struct {
	runner *Runner
	engine *snapshot.KopiaEngine
	reader *fakeReader
	jobs   *jobs.MemoryStore
	dk     []byte
	repo   snapshot.Repo
}

func newPipeline(t *testing.T) *pipeline {
	t.Helper()

	space := cs3.Space{
		ID:    testSpaceID,
		Name:  "Alice",
		Type:  "personal",
		Owner: "alice",
		Root:  cs3.ResourceID{StorageID: "storage-1", SpaceID: "space-1", OpaqueID: "root-1"},
	}
	reader := newFakeReader(space)
	reader.put("readme.txt", []byte("top-secret plaintext marker ALPHA"), testMTime)
	reader.put("docs/notes.txt", []byte("nested marker BRAVO"), testMTime)
	reader.put("фото/café.txt", []byte("unicode path content"), testMTime)

	engine, err := snapshot.NewEngine(snapshot.FilesystemOpener{Root: t.TempDir()}, snapshot.EngineOptions{
		WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	sealer := newSealer(t)
	wrapper := newSRWWrapper(t)
	configs := spacecfg.NewMemoryStore()
	targetStore := targets.NewMemoryStore()
	keyStore := keys.NewMemoryStore()
	jobStore := jobs.NewMemoryStoreWithClock(testutil.NewFakeClock(epoch))

	target := seedTarget(t, targetStore, sealer)
	seedConfig(t, configs, testSpaceID, testTargetID)
	dk := seedKeys(t, keyStore, wrapper, testSpaceID)

	runner, err := NewRunner(Deps{
		Spaces:  reader,
		Configs: configs,
		Targets: targetStore,
		Sealer:  sealer,
		Keys:    keyStore,
		Unwrap:  wrapper,
		Engine:  engine,
		Jobs:    jobStore,
		Locks:   jobStore,
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	return &pipeline{
		runner: runner,
		engine: engine,
		reader: reader,
		jobs:   jobStore,
		dk:     dk,
		repo: snapshot.Repo{
			Location: snapshot.Location{Bucket: target.Bucket, Prefix: target.Prefix},
			Space:    snapshot.SpaceRef{SpaceID: testSpaceID},
			DK:       dk,
		},
	}
}

func TestPipeline_BackupThenRestoreRoundTrips(t *testing.T) {
	ctx := context.Background()
	p := newPipeline(t)

	res, err := p.runner.RunBackup(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("RunBackup: %v", err)
	}
	if res.FileCount != 3 {
		t.Fatalf("file count = %d, want 3", res.FileCount)
	}

	out := t.TempDir()
	if err := p.engine.RestoreAll(ctx, p.repo, res.SnapshotID, out); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	for path, want := range map[string]string{
		"readme.txt":     "top-secret plaintext marker ALPHA",
		"docs/notes.txt": "nested marker BRAVO",
		"фото/café.txt":  "unicode path content",
	} {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read restored %s: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}

	// mtime is in backup scope (decisions.md #4).
	fi, err := os.Stat(filepath.Join(out, "readme.txt"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !fi.ModTime().Truncate(time.Second).Equal(testMTime.Truncate(time.Second)) {
		t.Fatalf("mtime = %v, want %v", fi.ModTime(), testMTime)
	}
}

func TestPipeline_SecondRunOnlyReadsChangedFiles(t *testing.T) {
	ctx := context.Background()
	p := newPipeline(t)

	if _, err := p.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("first RunBackup: %v", err)
	}
	unchangedReads := p.reader.openCount("readme.txt")

	p.reader.put("docs/notes.txt", []byte("nested marker BRAVO plus an edit"), testMTime.Add(time.Hour))

	res, err := p.runner.RunBackup(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("second RunBackup: %v", err)
	}

	if got := p.reader.openCount("readme.txt"); got != unchangedReads {
		t.Fatalf("unchanged file re-read from CS3 (%d -> %d); dedup cache unused", unchangedReads, got)
	}
	if got := p.reader.openCount("docs/notes.txt"); got != 2 {
		t.Fatalf("changed file read %d times, want 2", got)
	}

	out := t.TempDir()
	if err := p.engine.RestoreAll(ctx, p.repo, res.SnapshotID, out); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, "docs", "notes.txt"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(got) != "nested marker BRAVO plus an edit" {
		t.Fatalf("restored content = %q", got)
	}
}

func TestPipeline_SnapshotsAreListedPerSpace(t *testing.T) {
	ctx := context.Background()
	p := newPipeline(t)

	first, err := p.runner.RunBackup(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("RunBackup: %v", err)
	}
	p.reader.put("readme.txt", []byte("changed"), testMTime.Add(time.Hour))
	second, err := p.runner.RunBackup(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("RunBackup 2: %v", err)
	}

	list, err := p.engine.List(ctx, p.repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 snapshots, got %d", len(list))
	}
	if list[0].ID != second.SnapshotID || list[1].ID != first.SnapshotID {
		t.Fatalf("snapshots not newest-first: %+v", list)
	}
}
