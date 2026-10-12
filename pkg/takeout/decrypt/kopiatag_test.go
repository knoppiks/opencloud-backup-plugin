//go:build !testing

package decrypt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kopia/kopia/repo/format"

	"github.com/knoppiks/opencloud-backup-plugin/pkg/takeout"
)

// kopiaTestingTag: see kopiatag_testing_test.go.
const kopiaTestingTag = false

// The unit tests run with kopia's `testing` tag, so the repositories they
// create skip the release key derivation (#94). What keeps that derivation
// covered is a frozen Take-Out written with it, opened by every run; this
// test, which only a build without the tag can make (the integration jobs,
// a plain `go test`), fails when no fixture uses it, e.g. after a kopia
// upgrade that changed the default.
func TestFrozenTakeOutsCoverTheReleaseKeyDerivation(t *testing.T) {
	var used []string
	for _, name := range frozenTakeOuts {
		algo := frozenKeyDerivation(t, name)
		if algo == format.DefaultKeyDerivationAlgorithm {
			return
		}
		used = append(used, algo)
	}
	t.Fatalf("no frozen Take-Out derives its key with the release default %q (frozen: %v); freeze one (see frozen_test.go)",
		format.DefaultKeyDerivationAlgorithm, used)
}

// frozenKeyDerivation reads the key derivation a frozen Take-Out's repository
// was written with, from kopia's format blob (plain JSON).
func frozenKeyDerivation(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(frozenDir, name)
	m, err := takeout.ReadManifest(dir)
	if err != nil {
		t.Fatalf("%s: ReadManifest: %v", name, err)
	}
	raw, err := os.ReadFile(filepath.Join(m.RepoPath(dir), "kopia.repository.f"))
	if err != nil {
		t.Fatalf("%s: read format blob: %v", name, err)
	}
	blob, err := format.ParseKopiaRepositoryJSON(raw)
	if err != nil {
		t.Fatalf("%s: parse format blob: %v", name, err)
	}
	return blob.KeyDerivationAlgorithm
}
