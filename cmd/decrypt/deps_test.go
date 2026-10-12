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

// thisModule is the module path, the root of every package of this project.
const thisModule = "github.com/knoppiks/opencloud-backup-plugin"

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
	"github.com/knoppiks/opencloud-backup-plugin/pkg/keys",
	"github.com/knoppiks/opencloud-backup-plugin/pkg/takeout",
	"github.com/knoppiks/opencloud-backup-plugin/pkg/takeout/decrypt",
}

// thirdPartyModules is every module outside this one that decrypt is built
// from, without versions. Almost all come in through kopia. It is a tripwire
// for dependency upgrades: a kopia release that pulls in a new module (a new
// network client, say) fails here, and so does one that stops needing one,
// which is the moment to tighten s3Side or this list. Notably the Azure SDK
// and Prometheus are here only because kopia's core imports them for every
// storage, the local filesystem included (decisions.md, 10.6); re-check that
// on each kopia upgrade.
var thirdPartyModules = []string{
	"github.com/Azure/azure-sdk-for-go/sdk/azcore", // kopia repo/blob imports azblob error types (every storage)
	"github.com/Azure/azure-sdk-for-go/sdk/internal",
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob",
	"github.com/beorn7/perks",
	"github.com/cespare/xxhash/v2",
	"github.com/chmduquesne/rollinghash",
	"github.com/edsrzf/mmap-go",
	"github.com/go-logr/logr",
	"github.com/go-logr/stdr",
	"github.com/gofrs/flock",
	"github.com/google/uuid",
	"github.com/hashicorp/cronexpr",
	"github.com/klauspost/compress",
	"github.com/klauspost/cpuid/v2",
	"github.com/klauspost/pgzip",
	"github.com/klauspost/reedsolomon",
	"github.com/kopia/kopia", // the repository reader
	"github.com/munnerz/goautoneg",
	"github.com/natefinch/atomic",
	"github.com/petar/GoLLRB",
	"github.com/pkg/errors",
	"github.com/prometheus/client_golang", // kopia repo imports its metrics package
	"github.com/prometheus/client_model",
	"github.com/prometheus/common",
	"github.com/prometheus/procfs",
	"github.com/zeebo/blake3",
	"go.opentelemetry.io/auto/sdk",
	"go.opentelemetry.io/otel",
	"go.opentelemetry.io/otel/metric",
	"go.opentelemetry.io/otel/trace",
	"go.uber.org/multierr",
	"go.uber.org/zap",
	"golang.org/x/crypto",
	"golang.org/x/exp",
	"golang.org/x/net",
	"golang.org/x/sync",
	"golang.org/x/sys",
	"golang.org/x/term", // no-echo Recovery Key prompt
	"golang.org/x/text",
	"golang.org/x/time",
	"google.golang.org/genproto/googleapis/rpc",
	"google.golang.org/grpc", // kopia core (grpcapi, via metrics and repo)
	"google.golang.org/protobuf",
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

	got := testutil.DepsUnder(deps, thisModule)
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

func TestBinaryIsBuiltFromKnownModulesOnly(t *testing.T) {
	var got []string
	for _, m := range testutil.ModuleDeps(t, ".") {
		if m != thisModule {
			got = append(got, m)
		}
	}
	if !slices.Equal(got, thirdPartyModules) {
		t.Errorf("decrypt is built from the modules\n  %v\nwant\n  %v\n"+
			"(after a dependency upgrade: check what changed and why, then update thirdPartyModules)",
			got, thirdPartyModules)
	}
}
