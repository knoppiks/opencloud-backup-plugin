package memfs

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInspect(t *testing.T) {
	if runtime.GOOS != "linux" {
		if _, err := Inspect(t.TempDir()); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("off Linux the answer must be ErrUnsupported, got %v", err)
		}
		return
	}

	// /dev/shm is tmpfs on every Linux this service runs on; it stands in for
	// the deployment's memory-backed work directory.
	if _, err := os.Stat("/dev/shm"); err == nil {
		fs, err := Inspect("/dev/shm")
		if err != nil {
			t.Fatalf("Inspect(/dev/shm): %v", err)
		}
		if !fs.Memory || fs.Size <= 0 {
			t.Fatalf("Inspect(/dev/shm) = %+v, want a sized memory-backed filesystem", fs)
		}
	}

	if _, err := Inspect(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("a missing directory must be an error")
	}
}

// Two directories on one filesystem report the same device, so a caller can
// count the filesystem once.
func TestInspect_SameFilesystemSameDevice(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	a, err := Inspect(parent)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Inspect(child)
	if err != nil {
		t.Fatal(err)
	}
	if a.Device != b.Device || a.Memory != b.Memory {
		t.Fatalf("parent %+v and child %+v disagree", a, b)
	}
}
