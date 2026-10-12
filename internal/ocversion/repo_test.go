package ocversion

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The tests in this file hold the rest of the repository to versions.yaml
// (review-2026-10.md O1, O5): the README may only claim what CI runs, and no
// other file may pin an OpenCloud image on its own.

const pinFile = "internal/ocversion/versions.yaml"

// repoRoot is the module root: this package sits two directories below it.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("no go.mod at %s: %v", root, err)
	}
	return root
}

// repoFiles lists what git would commit: tracked files plus untracked ones
// that are not ignored. That leaves out node_modules, the fixture's generated
// config/data (root-owned, unreadable on a dev box) and build output.
func repoFiles(t *testing.T, root string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "ls-files", "-z",
		"--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}

// exemptFromPinCheck says which files may mention an OpenCloud image: the pin
// file itself, and the planning docs, which record history ("measured on
// 7.3.0") rather than configure anything.
func exemptFromPinCheck(path string) bool {
	return path == pinFile || strings.HasPrefix(path, ".agents/")
}

var imageReference = regexp.MustCompile(`opencloudeu/opencloud(-rolling)?(:|@sha256)`)

func TestNoOtherOpenCloudPin(t *testing.T) {
	root := repoRoot(t)
	pins, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	var digests [][]byte
	for _, l := range pins.Legs {
		digests = append(digests, []byte(strings.TrimPrefix(l.Digest, "sha256:")))
	}

	for _, path := range repoFiles(t, root) {
		if exemptFromPinCheck(path) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			// A file deleted in the working tree is still listed as tracked.
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		if loc := imageReference.FindIndex(data); loc != nil {
			t.Errorf("%s:%d names an OpenCloud image; read it from %s instead",
				path, lineOf(data, loc[0]), pinFile)
		}
		for _, d := range digests {
			if i := bytes.Index(data, d); i >= 0 {
				t.Errorf("%s:%d repeats a pinned OpenCloud digest; read it from %s instead",
					path, lineOf(data, i), pinFile)
			}
		}
	}
}

func lineOf(data []byte, offset int) int {
	return bytes.Count(data[:offset], []byte("\n")) + 1
}

func TestREADMESupportedSentence(t *testing.T) {
	pins, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile(filepath.Join(repoRoot(t), "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := pins.SupportedSentence()
	if !bytes.Contains(readme, []byte(want)) {
		t.Errorf("README.md does not state the tested range; it must contain:\n%s", want)
	}
}

// The fixture must take its image from up.sh, which reads the pin file. The
// pin check above catches a literal; this catches a hard-coded variable name
// drifting apart between the two scripts.
func TestFixtureReadsThePinnedImage(t *testing.T) {
	root := repoRoot(t)
	for path, want := range map[string]string{
		"test/fixtures/opencloud/docker-compose.yml": "image: ${OC_IMAGE:?",
		"test/fixtures/opencloud/up.sh":              pinFile,
	} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("%s does not contain %q", path, want)
		}
	}
}
