package testutil

import (
	"slices"
	"testing"
)

func TestDepsUnderMatchesWholePathSegments(t *testing.T) {
	deps := map[string]bool{
		"github.com/aws":                true,
		"github.com/aws/smithy-go":      true,
		"github.com/awsome/thing":       true,
		"example.org/mod/pkg/objstore":  true,
		"example.org/mod/pkg/objstores": true,
		"fmt":                           true,
	}

	got := DepsUnder(deps, "github.com/aws", "example.org/mod/pkg/objstore")
	want := []string{"example.org/mod/pkg/objstore", "github.com/aws", "github.com/aws/smithy-go"}
	if !slices.Equal(got, want) {
		t.Fatalf("DepsUnder = %v, want %v", got, want)
	}
}

func TestDepsUnderWithNothingToFind(t *testing.T) {
	if got := DepsUnder(map[string]bool{"fmt": true}, "github.com/aws"); len(got) != 0 {
		t.Fatalf("DepsUnder = %v, want none", got)
	}
	if got := DepsUnder(map[string]bool{"fmt": true}); len(got) != 0 {
		t.Fatalf("DepsUnder without roots = %v, want none", got)
	}
}

func TestPackageDepsListsTransitiveImports(t *testing.T) {
	deps := PackageDeps(t, ".")
	// This package imports os/exec, which imports os: both direct and
	// transitive dependencies are reported, and the package itself.
	for _, want := range []string{"os/exec", "os", "github.com/knoppiks/opencloud-backup-plugin/internal/testutil"} {
		if !deps[want] {
			t.Errorf("PackageDeps(.) is missing %s", want)
		}
	}
}
