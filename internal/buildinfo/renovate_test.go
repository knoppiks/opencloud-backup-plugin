package buildinfo

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Renovate keeps the pins current (phase-11 §11.6, #116). It finds the
// versions in workflows, the Makefile and scripts only through a
// `# renovate:` line directly above them (renovate.json, "annotated
// versions" and "annotated images"). A pin added without one is never
// bumped and rots unnoticed; these tests notice instead.

// pinnedFiles are the files that pin tool versions in variables, and the
// form a pin takes in each.
var pinnedFiles = []struct {
	glob string
	pin  *regexp.Regexp
}{
	{".github/workflows/*.yml", regexp.MustCompile(`^\s*[A-Z][A-Z0-9_]*_VERSION: "`)},
	{"Makefile", regexp.MustCompile(`^[A-Z][A-Z0-9_]*_(VERSION|IMAGE) \?= \S*\d`)},
	{"scripts/*.sh", regexp.MustCompile(`^[A-Z][A-Z0-9_]*_VERSION=\S*\d`)},
}

// The annotations renovate.json's custom managers read: a datasource and a
// package for a version, a datasource alone for an image reference.
var (
	versionAnnotation = regexp.MustCompile(`^\s*# renovate: datasource=[a-z-]+ depName=\S+( extractVersion=\S+)?$`)
	imageAnnotation   = regexp.MustCompile(`^# renovate: datasource=docker$`)
	pinnedImage       = regexp.MustCompile(`^[A-Z][A-Z0-9_]*_IMAGE \?= [^\s:@]+:[^\s@]+@sha256:[0-9a-f]{64}$`)
)

func TestEveryPinnedToolVersionIsAnnotatedForRenovate(t *testing.T) {
	pins := 0
	for _, f := range pinnedFiles {
		paths, err := filepath.Glob(filepath.Join(repoRoot, f.glob))
		if err != nil || len(paths) == 0 {
			t.Fatalf("%s: no files (%v)", f.glob, err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			lines := strings.Split(string(data), "\n")
			for i, line := range lines {
				if !f.pin.MatchString(line) {
					continue
				}
				pins++
				rel, _ := filepath.Rel(repoRoot, path)
				if i == 0 || !annotates(lines[i-1], line) {
					t.Errorf("%s:%d: %q has no `# renovate:` line above it that renovate.json can read",
						rel, i+1, strings.TrimSpace(line))
				}
			}
		}
	}
	// The globs must still find the pins: a test that checks nothing passes.
	if pins < 20 {
		t.Errorf("found only %d pinned versions; did a pin's form change?", pins)
	}
}

// annotates says whether annotation is the right kind for pin: an image
// reference by digest, or a version.
func annotates(annotation, pin string) bool {
	if strings.Contains(pin, "_IMAGE ") {
		return imageAnnotation.MatchString(annotation) && pinnedImage.MatchString(pin)
	}
	return versionAnnotation.MatchString(annotation)
}

// The integration tests start Garage from a constant, `make dev-up` from the
// dev compose file. Both are what the code is tested against.
func TestGarageIsTheSameEverywhere(t *testing.T) {
	inGo := toolchainIn(t, "internal/testutil/garage.go",
		regexp.MustCompile(`(?m)^const GarageImage = "([^"]+)"$`))
	inCompose := toolchainIn(t, "docker-compose.dev.yml",
		regexp.MustCompile(`(?m)^\s+image: (\S*garage\S*)$`))
	if inGo != inCompose {
		t.Errorf("internal/testutil/garage.go starts %s, docker-compose.dev.yml %s", inGo, inCompose)
	}
}
