package takeout

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knoppiks/opencloud-backup-plugin/internal/snapshot"
)

// copyFrozenTakeOut copies the committed v1 Take-Out (10.4) to a temporary
// directory, so a test can damage it without touching the fixture.
func copyFrozenTakeOut(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "takeout")
	if err := os.CopyFS(dst, os.DirFS(filepath.Join("testdata", "takeout-v1"))); err != nil {
		t.Fatalf("copy frozen take-out: %v", err)
	}
	return dst
}

func TestVerifyAcceptsAnIntactTakeOut(t *testing.T) {
	if err := Verify(context.Background(), copyFrozenTakeOut(t)); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	dir := copyFrozenTakeOut(t)

	// Overwrite the first blob file kopia wrote, whatever it sharded it to.
	tamperOneBlob(t, filepath.Join(dir, RepoDir))

	if err := Verify(context.Background(), dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

func TestVerifyDetectsAMissingEnvelope(t *testing.T) {
	dir := copyFrozenTakeOut(t)
	if err := os.Remove(filepath.Join(dir, EnvelopeFile)); err != nil {
		t.Fatalf("remove envelope: %v", err)
	}

	if err := Verify(context.Background(), dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

func TestVerifyRefusesAForeignDirectory(t *testing.T) {
	if err := Verify(context.Background(), t.TempDir()); !errors.Is(err, ErrNoTakeOut) {
		t.Fatalf("err = %v, want ErrNoTakeOut", err)
	}
}

// tamperOneBlob corrupts exactly one blob file inside a copied repository.
func tamperOneBlob(t *testing.T, repoDir string) {
	t.Helper()
	err := filepath.Walk(repoDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".f") {
			return err
		}
		if err := os.WriteFile(p, []byte("tampered"), 0o600); err != nil {
			return err
		}
		return io.EOF // stop after the first blob
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("no blob file found to tamper with: %v", err)
	}
}

func TestReadManifestRejectsForeignDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(`{"format":"something-else"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReadManifest(dir); !errors.Is(err, ErrNoTakeOut) {
		t.Fatalf("err = %v, want ErrNoTakeOut", err)
	}
}

// writeRawManifest writes a manifest with the given extra fields, valid
// otherwise.
func writeRawManifest(t *testing.T, version int, repoDir, envelopeRef string) string {
	t.Helper()
	dir := t.TempDir()
	if err := WriteManifest(dir, Manifest{
		Format:      ManifestFormat,
		Version:     version,
		SpaceID:     "space-1",
		RepoDir:     repoDir,
		EnvelopeRef: envelopeRef,
	}); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	return dir
}

// A Take-Out travels from the admin to a family member; its manifest must not
// be able to point decrypt outside the directory (review-2026-10.md F4).
func TestReadManifestRejectsPathsOutsideTheTakeOut(t *testing.T) {
	for name, tc := range map[string]struct{ repoDir, envelope string }{
		"repo parent":       {repoDir: "../elsewhere"},
		"repo absolute":     {repoDir: "/etc"},
		"repo nested up":    {repoDir: "repo/../../x"},
		"envelope parent":   {envelope: "../recovery.ocbke"},
		"envelope absolute": {envelope: "/home/user/.ssh/id_ed25519"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeRawManifest(t, ManifestVersion, tc.repoDir, tc.envelope)
			if _, err := ReadManifest(dir); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("ReadManifest = %v, want ErrCorrupt", err)
			}
		})
	}
}

// Verify reads every recorded blob by its id, so an id is a path too. The "."
// case was found by FuzzReadManifest.
func TestReadManifestRejectsBlobIDsOutsideTheRepository(t *testing.T) {
	for _, id := range []string{"", ".", "..", "../kopia.repository", "/etc/passwd", `..\x`, "p0/../../x"} {
		t.Run(id, func(t *testing.T) {
			dir := t.TempDir()
			if err := WriteManifest(dir, Manifest{
				Format: ManifestFormat, Version: ManifestVersion, SpaceID: "space-1",
				Blobs: []snapshot.BlobRef{{ID: "kopia.repository"}, {ID: id}},
			}); err != nil {
				t.Fatalf("WriteManifest: %v", err)
			}
			if _, err := ReadManifest(dir); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("ReadManifest = %v, want ErrCorrupt", err)
			}
		})
	}
}

// No takeout ever wrote a manifest without a version; one that has none was
// not written by takeout.
func TestReadManifestRejectsAMissingVersion(t *testing.T) {
	for _, version := range []int{0, -1} {
		dir := writeRawManifest(t, version, "", "")
		if _, err := ReadManifest(dir); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("version %d: ReadManifest = %v, want ErrCorrupt", version, err)
		}
	}
}

func TestReadManifestAcceptsLocalPaths(t *testing.T) {
	dir := writeRawManifest(t, ManifestVersion, "repo/sub", "keys/recovery.ocbke")
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got, want := m.RepoPath(dir), filepath.Join(dir, "repo", "sub"); got != want {
		t.Fatalf("RepoPath = %q, want %q", got, want)
	}
	if got, want := m.EnvelopePath(dir), filepath.Join(dir, "keys", "recovery.ocbke"); got != want {
		t.Fatalf("EnvelopePath = %q, want %q", got, want)
	}
}

func TestReadManifestReportsANewerFormat(t *testing.T) {
	dir := writeRawManifest(t, ManifestVersion+1, "", "")
	_, err := ReadManifest(dir)
	if !errors.Is(err, ErrNewerTakeOut) {
		t.Fatalf("ReadManifest = %v, want ErrNewerTakeOut", err)
	}
	if errors.Is(err, ErrCorrupt) {
		t.Fatal("a newer Take-Out is not a damaged one")
	}
}
