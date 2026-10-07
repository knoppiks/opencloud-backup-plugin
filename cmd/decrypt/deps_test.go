package main

// decrypt runs on a family member's own machine, offline, possibly years after
// the backup was written. What it is built from is part of that promise: every
// dependency is one more thing that can break its build or has to be trusted
// there. These tests pin it (phase 10.6).

import (
	"slices"
	"testing"

	"github.com/knoppiks/opencloud-backup-plugin/internal/testutil"
)

// s3Side are the import paths of everything that reaches a storage target over
// the network. decrypt reads a Take-Out from a local directory and needs none
// of it.
var s3Side = []string{
	// S3 clients.
	"github.com/aws",
	"github.com/minio",
	// kopia's storage drivers other than the local filesystem. kopia's core
	// (repo/blob) imports the Azure SDK's error types and Prometheus for its
	// metrics on its own, so those two modules cannot be kept out without
	// reimplementing the repository reader; its *drivers* can.
	"github.com/kopia/kopia/repo/blob/s3",
	"github.com/kopia/kopia/repo/blob/azure",
	"github.com/kopia/kopia/repo/blob/gcs",
	"github.com/kopia/kopia/repo/blob/b2",
	"github.com/kopia/kopia/repo/blob/gdrive",
	"github.com/kopia/kopia/repo/blob/sftp",
	"github.com/kopia/kopia/repo/blob/webdav",
	"github.com/kopia/kopia/repo/blob/rclone",
	// This project's target-facing packages.
	"github.com/knoppiks/opencloud-backup-plugin/internal/objstore",
	"github.com/knoppiks/opencloud-backup-plugin/internal/snapshot/s3repo",
	"github.com/knoppiks/opencloud-backup-plugin/internal/takeout/remote",
}

// ownPackages is every package of this module decrypt may be built from. A new
// entry is a deliberate decision, made here and reviewed, not a side effect of
// an import added somewhere below.
var ownPackages = []string{
	"github.com/knoppiks/opencloud-backup-plugin/cmd/decrypt",
	"github.com/knoppiks/opencloud-backup-plugin/internal/buildinfo",
	"github.com/knoppiks/opencloud-backup-plugin/internal/cli",
	"github.com/knoppiks/opencloud-backup-plugin/internal/snapshot",
	"github.com/knoppiks/opencloud-backup-plugin/internal/state",
	"github.com/knoppiks/opencloud-backup-plugin/pkg/keys",
	"github.com/knoppiks/opencloud-backup-plugin/pkg/takeout",
	"github.com/knoppiks/opencloud-backup-plugin/pkg/takeout/decrypt",
}

func TestBinaryLinksNoStorageClient(t *testing.T) {
	deps := testutil.PackageDeps(t, ".")

	if linked := testutil.DepsUnder(deps, s3Side...); len(linked) > 0 {
		t.Errorf("decrypt links target-facing code it never uses:\n  %v", linked)
	}

	// A control: the names above must be the ones the S3 side really has. The
	// admin's take-out tool reaches the target, so it links them; if a rename
	// made the list stale, the check above would pass vacuously.
	adminSide := testutil.PackageDeps(t, "../takeout")
	for _, root := range []string{
		"github.com/aws", "github.com/minio", "github.com/kopia/kopia/repo/blob/s3",
		"github.com/knoppiks/opencloud-backup-plugin/internal/objstore",
		"github.com/knoppiks/opencloud-backup-plugin/internal/snapshot/s3repo",
		"github.com/knoppiks/opencloud-backup-plugin/internal/takeout/remote",
	} {
		if len(testutil.DepsUnder(adminSide, root)) == 0 {
			t.Errorf("takeout no longer links %s; the forbidden list above is stale", root)
		}
	}
}

func TestBinaryIsBuiltFromTheFormatPackagesOnly(t *testing.T) {
	deps := testutil.PackageDeps(t, ".")

	got := testutil.DepsUnder(deps, "github.com/knoppiks/opencloud-backup-plugin")
	if !slices.Equal(got, ownPackages) {
		t.Errorf("decrypt is built from\n  %v\nwant\n  %v\n"+
			"(adding a package to decrypt is a decision: update ownPackages with the reason)",
			got, ownPackages)
	}
	// It still opens repositories from a local directory.
	if !deps["github.com/kopia/kopia/repo/blob/filesystem"] {
		t.Error("decrypt no longer links kopia's filesystem storage; it cannot open a Take-Out")
	}
}
