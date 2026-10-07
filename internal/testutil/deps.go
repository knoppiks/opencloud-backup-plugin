package testutil

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// PackageDeps returns the full transitive dependency set of a package as
// `go list -deps` reports it: what the package's binary links, test files
// excluded. pkg is anything `go list` accepts, usually a relative directory.
//
// Tests that audit what a binary is built from use it (the admin tool must
// not link the decrypt path; the decrypt tool must not link an S3 client). It
// skips the test when no go toolchain is on PATH, since then the question
// cannot be asked.
func PackageDeps(t testing.TB, pkg string) map[string]bool {
	t.Helper()

	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}

	out, err := exec.Command(goBin, "list", "-deps", pkg).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	deps := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		deps[line] = true
	}
	return deps
}

// DepsUnder returns, sorted, the import paths in deps that are one of roots or
// sit below one of them. A root names a package or module path without a
// trailing slash: "github.com/aws" matches "github.com/aws/smithy-go" but not
// "github.com/awsome".
func DepsUnder(deps map[string]bool, roots ...string) []string {
	var found []string
	for dep := range deps {
		for _, root := range roots {
			if dep == root || strings.HasPrefix(dep, root+"/") {
				found = append(found, dep)
				break
			}
		}
	}
	slices.Sort(found)
	return found
}
