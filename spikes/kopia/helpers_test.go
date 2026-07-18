//go:build integration

package main

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeSourceTree writes a varied directory tree (nested dirs, non-ASCII names,
// a large file to exercise multipart/dedup, known plaintext markers, explicit
// mtimes) and returns its root path.
func makeSourceTree(t *testing.T, work string) string {
	t.Helper()
	root := filepath.Join(work, "source")

	// Explicit mtime in the past so we can assert it is preserved on restore.
	mtime := time.Date(2021, 3, 14, 15, 9, 26, 0, time.UTC)

	writeFile(t, filepath.Join(root, "readme.txt"),
		[]byte("top-secret plaintext marker ALPHA\n"), mtime)
	writeFile(t, filepath.Join(root, "docs", "notes.txt"),
		[]byte("nested marker BRAVO with more text\n"), mtime)
	// Non-ASCII directory + filename.
	writeFile(t, filepath.Join(root, "фото", "café.txt"),
		[]byte("unicode path content\n"), mtime)

	// Large (24 MiB) random binary with a non-ASCII name -> multipart + dedup.
	big := make([]byte, 24<<20)
	if _, err := rand.Read(big); err != nil {
		t.Fatalf("rand: %v", err)
	}
	// Embed a marker at a known offset to prove content is encrypted at rest.
	copy(big[1000:], []byte("große-datei"))
	writeFile(t, filepath.Join(root, "assets", "große-datei.bin"), big, mtime)

	return root
}

func writeFile(t *testing.T, path string, data []byte, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func appendToFile(t *testing.T, path string, extra []byte) {
	t.Helper()
	existing, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(existing, extra...), 0o644); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
}

// assertTreesEqual walks want and asserts every regular file exists under got
// with identical bytes and mtime (truncated to seconds; kopia stores second
// granularity for restore comparison here).
func assertTreesEqual(t *testing.T, want, got string) {
	t.Helper()
	err := filepath.Walk(want, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(want, p)
		if err != nil {
			return err
		}
		gp := filepath.Join(got, rel)
		gi, err := os.Lstat(gp)
		if err != nil {
			t.Fatalf("missing in restore: %s: %v", rel, err)
		}
		if info.IsDir() {
			return nil
		}
		assertFilesEqual(t, p, gp)
		// mtime preserved (second granularity).
		if !info.ModTime().Truncate(time.Second).Equal(gi.ModTime().Truncate(time.Second)) {
			t.Fatalf("mtime mismatch for %s: want %s got %s",
				rel, info.ModTime(), gi.ModTime())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func assertFilesEqual(t *testing.T, want, got string) {
	t.Helper()
	a, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read want %s: %v", want, err)
	}
	b, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read got %s: %v", got, err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("content mismatch: %s vs %s (%d vs %d bytes)", want, got, len(a), len(b))
	}
}

func stripScheme(endpoint string) string {
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	return endpoint
}
