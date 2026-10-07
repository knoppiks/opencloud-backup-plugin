package buildinfo

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The Go toolchain is named in four places: go.mod (what `go` itself switches
// to), the image's build stage, and the workflows' GO_VERSION. What CI tests
// has to be what the image ships, so they must name the same patch release
// (phase 10.7, review G3). Renovate bumps them together from Phase 11; this
// test catches a bump that missed one.

const repoRoot = "../.."

var toolchainSources = []struct {
	path    string
	pattern *regexp.Regexp
}{
	{"go.mod", regexp.MustCompile(`(?m)^toolchain go(\d+\.\d+\.\d+)$`)},
	{"Dockerfile", regexp.MustCompile(`(?m)^FROM .*\bgolang:(\d+\.\d+\.\d+)-`)},
	{".github/workflows/ci.yml", regexp.MustCompile(`(?m)^\s*GO_VERSION: "(\d+\.\d+\.\d+)"$`)},
	{".github/workflows/fuzz.yml", regexp.MustCompile(`(?m)^\s*GO_VERSION: "(\d+\.\d+\.\d+)"$`)},
	{".github/workflows/canary.yml", regexp.MustCompile(`(?m)^\s*GO_VERSION: "(\d+\.\d+\.\d+)"$`)},
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
