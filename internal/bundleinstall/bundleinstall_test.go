package bundleinstall

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func bundle() fstest.MapFS {
	return fstest.MapFS{
		"manifest.json":       {Data: []byte(`{"entrypoint":"js/backup-vault.mjs"}`), Mode: 0o644},
		"js/backup-vault.mjs": {Data: []byte("export {}"), Mode: 0o644},
		"assets/src.css":      {Data: []byte("body{}"), Mode: 0o644},
		"THIRD_PARTY_NOTICES": {Data: []byte("notices"), Mode: 0o644},
		"js/nested/chunk.mjs": {Data: []byte("chunk"), Mode: 0o600},
		"assets/empty-dir":    {Mode: fs.ModeDir | 0o755},
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInstallCopiesTheTreeAndCreatesTheDestination(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "extensions", "backup-vault")

	res, err := Install(bundle(), dst)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 5 {
		t.Errorf("Files = %d, want 5", res.Files)
	}
	var want int64
	for _, f := range bundle() {
		want += int64(len(f.Data))
	}
	if res.Bytes != want {
		t.Errorf("Bytes = %d, want %d", res.Bytes, want)
	}
	if got := readFile(t, filepath.Join(dst, "js", "nested", "chunk.mjs")); got != "chunk" {
		t.Errorf("chunk = %q", got)
	}
	if info, err := os.Stat(filepath.Join(dst, "assets", "empty-dir")); err != nil || !info.IsDir() {
		t.Errorf("empty directory not created: %v", err)
	}
	info, err := os.Stat(filepath.Join(dst, "js", "nested", "chunk.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v, want the source's 0600", perm)
	}
}

// A pod restart over a volume that kept an earlier copy: the bundle's files
// are replaced, others stay, as `cp -R` does.
func TestInstallOverwritesTheBundlesFilesAndLeavesOthers(t *testing.T) {
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, "manifest.json"), []byte("an older, longer manifest"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "stale.mjs"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Install(bundle(), dst); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dst, "manifest.json")); got != string(bundle()["manifest.json"].Data) {
		t.Errorf("manifest.json = %q, not replaced", got)
	}
	if got := readFile(t, filepath.Join(dst, "stale.mjs")); got != "old" {
		t.Errorf("stale.mjs = %q, want it left alone", got)
	}
}

func TestInstallRefusesASymlinkInTheBundle(t *testing.T) {
	src := bundle()
	src["js/link.mjs"] = &fstest.MapFile{Data: []byte("/etc/passwd"), Mode: fs.ModeSymlink | 0o777}

	_, err := Install(src, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "js/link.mjs: not a regular file") {
		t.Fatalf("err = %v, want a refusal naming js/link.mjs", err)
	}
}

// Something already in the volume must not redirect a write outside it.
func TestInstallDoesNotFollowASymlinkOutOfTheDestination(t *testing.T) {
	outside := t.TempDir()
	dst := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dst, "js")); err != nil {
		t.Fatal(err)
	}

	if _, err := Install(bundle(), dst); err == nil {
		t.Fatal("Install followed a symlink out of the destination")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("wrote %d entries outside the destination", len(entries))
	}
}

func TestInstallRefusesAnEmptyBundle(t *testing.T) {
	_, err := Install(fstest.MapFS{"js": {Mode: fs.ModeDir | 0o755}}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v, want an empty-bundle error", err)
	}
}

func TestInstallFailsWhenTheDestinationIsAFile(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(dst, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(bundle(), dst); err == nil {
		t.Fatal("Install into a file succeeded")
	}
}
