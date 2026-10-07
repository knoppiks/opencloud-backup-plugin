package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWorkDirOrTemp(t *testing.T) {
	if got := WorkDirOrTemp(""); got != os.TempDir() {
		t.Fatalf("empty work dir = %q, want the OS temp dir", got)
	}
	if got := WorkDirOrTemp("/var/run/backup"); got != "/var/run/backup" {
		t.Fatalf("work dir = %q", got)
	}
}

func TestSweepWorkDir_RemovesOnlyRunDirectories(t *testing.T) {
	dir := t.TempDir()
	leftovers := []string{"kopia-run-123", "kopia-run-456"}
	for _, name := range leftovers {
		if err := os.MkdirAll(filepath.Join(dir, name, "cache"), 0o700); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	keep := filepath.Join(dir, "something-else")
	if err := os.MkdirAll(keep, 0o700); err != nil {
		t.Fatalf("seed keeper: %v", err)
	}

	removed, err := SweepWorkDir(dir)
	if err != nil {
		t.Fatalf("SweepWorkDir: %v", err)
	}
	if removed != len(leftovers) {
		t.Fatalf("removed %d, want %d", removed, len(leftovers))
	}
	for _, name := range leftovers {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived the sweep", name)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("an unrelated directory was removed: %v", err)
	}
}

func TestSweepWorkDir_EmptyDirectoryIsNotAnError(t *testing.T) {
	removed, err := SweepWorkDir(t.TempDir())
	if err != nil || removed != 0 {
		t.Fatalf("SweepWorkDir on a clean dir = (%d, %v)", removed, err)
	}
}

func TestMemoryBacked(t *testing.T) {
	if runtime.GOOS != "linux" {
		if _, err := MemoryBacked(t.TempDir()); !errors.Is(err, ErrWorkDirFilesystemUnknown) {
			t.Fatalf("off Linux the answer must be 'unknown', got %v", err)
		}
		return
	}

	// /dev/shm is tmpfs on every Linux this service runs on; it stands in for
	// the deployment's memory-backed emptyDir.
	if _, err := os.Stat("/dev/shm"); err == nil {
		got, err := MemoryBacked("/dev/shm")
		if err != nil {
			t.Fatalf("MemoryBacked(/dev/shm): %v", err)
		}
		if !got {
			t.Fatal("/dev/shm was not recognised as memory-backed")
		}
	}

	if _, err := MemoryBacked(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("a missing work directory must be an error, not a silent false")
	}
}
