package buildinfo

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The Go toolchain is named in several places: go.mod (what `go` itself
// switches to), the images' build stages, and the workflows' GO_VERSION.
// What CI tests has to be what the images ship, so they must name the same
// patch release
// (phase 10.7, review G3). Renovate bumps them together from Phase 11; this
// test catches a bump that missed one.

const repoRoot = "../.."

var toolchainSources = []struct {
	path    string
	pattern *regexp.Regexp
}{
	{"go.mod", regexp.MustCompile(`(?m)^toolchain go(\d+\.\d+\.\d+)$`)},
	{"Dockerfile", regexp.MustCompile(`(?m)^FROM .*\bgolang:(\d+\.\d+\.\d+)-`)},
	{"web/Dockerfile", regexp.MustCompile(`(?m)^FROM .*\bgolang:(\d+\.\d+\.\d+)-`)},
	{".github/workflows/ci.yml", regexp.MustCompile(`(?m)^\s*GO_VERSION: "(\d+\.\d+\.\d+)"$`)},
	{".github/workflows/fuzz.yml", regexp.MustCompile(`(?m)^\s*GO_VERSION: "(\d+\.\d+\.\d+)"$`)},
	{".github/workflows/canary.yml", regexp.MustCompile(`(?m)^\s*GO_VERSION: "(\d+\.\d+\.\d+)"$`)},
	{".github/workflows/release.yml", regexp.MustCompile(`(?m)^\s*GO_VERSION: "(\d+\.\d+\.\d+)"$`)},
}

// The release's other tools are pinned in ci.yml, which builds the release
// snapshot on every pull request, and in release.yml, which builds the
// release (#104). The snapshot proves only what the release will use if both
// name the same versions.
func TestReleaseToolsAreTheSameInCIAndRelease(t *testing.T) {
	for _, name := range []string{"NODE_VERSION", "GORELEASER_VERSION", "SYFT_VERSION"} {
		pattern := regexp.MustCompile(`(?m)^\s*` + name + `: "([^"]+)"$`)
		ci := toolchainIn(t, ".github/workflows/ci.yml", pattern)
		release := toolchainIn(t, ".github/workflows/release.yml", pattern)
		if ci != release {
			t.Errorf("%s: ci.yml pins %s, release.yml pins %s", name, ci, release)
		}
	}
}

// The images are signed by the cosign release.yml pins, verified by the one
// release-publish.yml pins, and README and the release notes tell users which
// major they need (#108): an older major does not find the signatures.
func TestCosignIsTheSameWhereItSignsAndVerifies(t *testing.T) {
	pattern := regexp.MustCompile(`(?m)^\s*COSIGN_VERSION: "v(\d+)\.\d+\.\d+"$`)
	signs := toolchainIn(t, ".github/workflows/release.yml", pattern)
	verifies := toolchainIn(t, ".github/workflows/release-publish.yml", pattern)
	if signs != verifies {
		t.Errorf("release.yml pins cosign v%s.x, release-publish.yml v%s.x", signs, verifies)
	}
	want := "cosign v" + signs + " or newer"
	for _, path := range []string{"README.md", "scripts/release-notes.sh"} {
		data, err := os.ReadFile(filepath.Join(repoRoot, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !regexp.MustCompile(`(?i)` + regexp.QuoteMeta(want)).Match(data) {
			t.Errorf("%s does not say %q for image verification", path, want)
		}
	}
}

func TestToolchainIsTheSameEverywhere(t *testing.T) {
	want := ""
	for _, src := range toolchainSources {
		got := toolchainIn(t, src.path, src.pattern)
		if want == "" {
			want = got
			continue
		}
		if got != want {
			t.Errorf("%s names Go %s, %s names Go %s", src.path, got, toolchainSources[0].path, want)
		}
	}
}

// toolchainIn returns the single Go version pattern finds in the file. A file
// naming two would leave the check above comparing only one of them.
func toolchainIn(t *testing.T, path string, pattern *regexp.Regexp) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	matches := pattern.FindAllSubmatch(data, -1)
	if len(matches) != 1 {
		t.Fatalf("%s: want exactly one Go version matching %s, found %d", path, pattern, len(matches))
	}
	return string(matches[0][1])
}
