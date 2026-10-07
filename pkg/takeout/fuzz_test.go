package takeout

// Fuzz target for the Take-Out manifest, which the compatibility policy
// promises decrypt reads forever. A manifest is a file handed from an admin to
// a family member, so it is untrusted input: whatever it says, the parser must
// answer with one of the Take-Out errors or a manifest whose every path stays
// inside the directory (review-2026-10.md F4).

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knoppiks/opencloud-backup-plugin/internal/snapshot"
)

func FuzzReadManifest(f *testing.F) {
	frozen, err := filepath.Glob(filepath.Join("testdata", "*", ManifestFile))
	if err != nil {
		f.Fatal(err)
	}
	for _, path := range frozen {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	for _, m := range []Manifest{
		{Format: ManifestFormat, Version: ManifestVersion, SpaceID: "s", RepoDir: "repo",
			Blobs: []snapshot.BlobRef{{ID: "kopia.repository", Size: 1, SHA256: "00"}}},
		{Format: ManifestFormat, Version: ManifestVersion + 1, SpaceID: "s"},
		{Format: ManifestFormat, Version: ManifestVersion, SpaceID: "s", RepoDir: "../x"},
		{Format: ManifestFormat, Version: ManifestVersion, SpaceID: "s", EnvelopeRef: "/etc/passwd"},
		{Format: ManifestFormat, Version: ManifestVersion, SpaceID: "s",
			Blobs: []snapshot.BlobRef{{ID: "../../kopia.repository"}}},
		{Format: "something-else", Version: 1},
	} {
		data, err := json.Marshal(m)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte("{}"))
	f.Add([]byte("not json"))

	const dir = "/media/usb/takeout"

	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := parseManifest(data)
		if err != nil {
			if !errors.Is(err, ErrNoTakeOut) && !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrNewerTakeOut) {
				t.Fatalf("error %v is not a Take-Out error", err)
			}
			return
		}
		if m.Format != ManifestFormat || m.Version < 1 || m.Version > ManifestVersion || m.SpaceID == "" {
			t.Fatalf("accepted a manifest it cannot read: format %q version %d space %q", m.Format, m.Version, m.SpaceID)
		}
		for _, p := range []string{m.RepoPath(dir), m.EnvelopePath(dir)} {
			if !inside(dir, p) {
				t.Fatalf("accepted a manifest naming %q, outside %q", p, dir)
			}
		}
		repo := m.RepoPath(dir)
		for _, b := range m.Blobs {
			if p := filepath.Join(repo, b.ID); !inside(repo, p) || p == repo {
				t.Fatalf("accepted blob id %q, which is not a file in the repository", b.ID)
			}
		}
	})
}

// inside reports whether path is dir or below it.
func inside(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
