package buildinfo

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The workflows' invariants zizmor and actionlint do not check (phase-11
// §11.7, #125): every job that runs steps has a time limit, and every
// action outside this repository is pinned to a commit with the version
// Renovate keeps beside it (`@<sha> # vX.Y.Z`). The repository refuses
// unpinned actions too, but only when a workflow runs; this fails first.

type workflow struct {
	Jobs map[string]struct {
		Uses           string `yaml:"uses"`
		TimeoutMinutes any    `yaml:"timeout-minutes"`
	} `yaml:"jobs"`
}

func workflowFiles(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(repoRoot, ".github/workflows/*.yml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no workflows (%v)", err)
	}
	return paths
}

func TestEveryJobHasATimeLimit(t *testing.T) {
	for _, path := range workflowFiles(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var wf workflow
		if err := yaml.Unmarshal(data, &wf); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for name, job := range wf.Jobs {
			// A job calling a reusable workflow cannot set one; the called
			// workflow's jobs do.
			if job.Uses == "" && job.TimeoutMinutes == nil {
				t.Errorf("%s: job %q has no timeout-minutes", filepath.Base(path), name)
			}
		}
	}
}

var (
	usesLine   = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*(\S+)(.*)$`)
	pinnedUses = regexp.MustCompile(`^[\w.-]+/[\w./-]+@[0-9a-f]{40}$`)
	versionTag = regexp.MustCompile(`^\s+# v\d+\.\d+\.\d+$`)
)

func TestEveryActionIsPinnedToACommit(t *testing.T) {
	refs := 0
	for _, path := range workflowFiles(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			m := usesLine.FindStringSubmatch(line)
			if m == nil || strings.HasPrefix(m[1], "./") {
				continue
			}
			refs++
			if !pinnedUses.MatchString(m[1]) || !versionTag.MatchString(m[2]) {
				t.Errorf("%s:%d: %q is not `owner/repo@<40-hex sha> # vX.Y.Z`",
					filepath.Base(path), i+1, strings.TrimSpace(line))
			}
		}
	}
	if refs < 50 {
		t.Errorf("found only %d action references; did their form change?", refs)
	}
}
