package snapshot

// Blob-level repository copy: the Take-Out's transport step. The properties
// that matter are that the copy is complete, that it reopens as a normal
// repository, and that damage is detectable.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedRepo creates a repository with one snapshot and returns its blob storage.
func seedRepo(t *testing.T) (*KopiaEngine, Repo, string) {
	t.Helper()
	root := t.TempDir()
	e, err := NewEngine(FilesystemOpener{Root: root}, EngineOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e, testRepo(t, "space-1"), root
}

func TestCopyRepo_ProducesAReopenableRepository(t *testing.T) {
	ctx := context.Background()
	engine, r, root := seedRepo(t)

	info, err := engine.Snapshot(ctx, r, treeSource(t))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	src, err := FilesystemOpener{Root: root}.Open(ctx, r, false)
	if err != nil {
		t.Fatalf("open source storage: %v", err)
	}
	defer func() { _ = src.Close(ctx) }()

	dest := filepath.Join(t.TempDir(), "repo")
	var lastCount int
	refs, err := CopyRepo(ctx, src, dest, func(count int, _ int64) { lastCount = count })
	if err != nil {
		t.Fatalf("CopyRepo: %v", err)
	}
	if len(refs) == 0 || lastCount != len(refs) {
		t.Fatalf("copied %d blobs, progress reported %d", len(refs), lastCount)
	}
	for _, ref := range refs {
		if ref.ID == "" || ref.SHA256 == "" || ref.Size == 0 {
			t.Fatalf("incomplete blob record: %+v", ref)
		}
	}

	// The copy is a normal repository: it restores with the same Data Key.
	offline, err := NewEngine(DirOpener{Dir: dest}, EngineOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewEngine(DirOpener): %v", err)
	}
	out := t.TempDir()
	if err := offline.RestoreAll(ctx, r, info.ID, out); err != nil {
		t.Fatalf("RestoreAll from copy: %v", err)
	}
	assertFileContent(t, filepath.Join(out, "readme.txt"), "hello")

	if err := VerifyRepoDir(ctx, dest, refs); err != nil {
		t.Fatalf("VerifyRepoDir: %v", err)
	}
}

func TestVerifyRepoDir_DetectsDamage(t *testing.T) {
	ctx := context.Background()
	engine, r, root := seedRepo(t)
	if _, err := engine.Snapshot(ctx, r, treeSource(t)); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	src, err := FilesystemOpener{Root: root}.Open(ctx, r, false)
	if err != nil {
		t.Fatalf("open source storage: %v", err)
	}
	defer func() { _ = src.Close(ctx) }()

	dest := filepath.Join(t.TempDir(), "repo")
	refs, err := CopyRepo(ctx, src, dest, nil)
	if err != nil {
		t.Fatalf("CopyRepo: %v", err)
	}

	// Truncate one blob: the digest check must notice.
	corrupted := false
	err = filepath.Walk(dest, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".f") || corrupted {
			return err
		}
		corrupted = true
		if err := os.WriteFile(p, []byte("damaged"), 0o600); err != nil {
			return err
		}
		return io.EOF
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("could not damage a blob: %v", err)
	}

	if err := VerifyRepoDir(ctx, dest, refs); err == nil {
		t.Fatal("a damaged blob must fail verification")
	}
}

func TestCopyRepo_Validation(t *testing.T) {
	ctx := context.Background()
	engine, r, root := seedRepo(t)

	if _, err := CopyRepo(ctx, nil, t.TempDir(), nil); err == nil {
		t.Fatal("a nil source must be rejected")
	}

	src, err := FilesystemOpener{Root: root}.Open(ctx, r, true)
	if err != nil {
		t.Fatalf("open source storage: %v", err)
	}
	defer func() { _ = src.Close(ctx) }()

	if _, err := CopyRepo(ctx, src, "", nil); err == nil {
		t.Fatal("an empty destination must be rejected")
	}
	// Nothing has been snapshotted yet: an empty repository is an error, not a
	// silently empty take-out.
	if _, err := CopyRepo(ctx, src, filepath.Join(t.TempDir(), "repo"), nil); err == nil {
		t.Fatal("copying an empty repository must fail")
	}

	if _, err := engine.Snapshot(ctx, r, treeSource(t)); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := VerifyRepoDir(ctx, filepath.Join(t.TempDir(), "absent"), nil); err == nil {
		t.Fatal("verifying a missing directory must fail")
	}
}
