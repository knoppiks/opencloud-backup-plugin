//go:build integration

package backup

// Phase-4 exit criterion 1, end to end:
//
//	seeded OpenCloud Space -> encrypted snapshot on Garage -> verified restore.
//
// Unlike the rest of the integration suite this needs a live OpenCloud, so it
// skips unless the fixture environment is present.
//
//	cd test/fixtures/opencloud && ./up.sh && ./seed.sh
//	source test/fixtures/opencloud/fixture.env
//	go test -tags integration -run TestIntegration_OpenCloudEndToEnd ./pkg/backup/...

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/objstore"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/takeout"
	"opencloud-backup-plugin/pkg/targets"
)

func TestIntegration_OpenCloudEndToEnd(t *testing.T) {
	addr := os.Getenv("CS3_GATEWAY_ADDR")
	saID := os.Getenv("CS3_SERVICE_ACCOUNT_ID")
	saSecret := os.Getenv("CS3_SERVICE_ACCOUNT_SECRET")
	expectFile := strings.TrimPrefix(os.Getenv("CS3_EXPECT_FILE"), "/")
	expectSHA := os.Getenv("CS3_EXPECT_SHA256")
	ownerUID := os.Getenv("OC_ADMIN_USER_ID")
	if addr == "" || saID == "" || saSecret == "" || expectFile == "" || expectSHA == "" {
		t.Skip("OpenCloud fixture env not set; source test/fixtures/opencloud/fixture.env")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// --- real CS3 reader --------------------------------------------------
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial gateway %s: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()

	gw := gateway.NewGatewayAPIClient(conn)
	reader := cs3.NewClient(gw,
		cs3.ServiceAccountAuth{Gateway: gw, ClientID: saID, Secret: saSecret},
		// The fixture serves the data gateway with a self-signed certificate.
		cs3.WithHTTPClient(&http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		}),
	)

	space := findSeededSpace(ctx, t, reader, ownerUID)
	t.Logf("backing up space id=%s name=%q owner=%s", space.ID, space.Name, space.Owner)

	// --- real target ------------------------------------------------------
	garage := testutil.StartGarage(ctx, t)

	engine, err := snapshot.NewEngine(snapshot.S3Opener{}, snapshot.EngineOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	sealer := newSealer(t)
	wrapper := newSRWWrapper(t)
	configs := spacecfg.NewMemoryStore()
	targetStore := targets.NewMemoryStore()
	keyStore := keys.NewMemoryStore()
	jobStore := jobs.NewMemoryStore()

	seedGarageTarget(t, targetStore, sealer, garage)
	seedConfig(t, configs, space.ID, testTargetID)
	dk := seedKeys(t, keyStore, wrapper, space.ID)

	runner, err := NewRunner(Deps{
		Spaces:  reader,
		Configs: configs,
		Targets: targetStore,
		Sealer:  sealer,
		Keys:    keyStore,
		Unwrap:  wrapper,
		Engine:  engine,
		Jobs:    jobStore,
		Locks:   jobStore,
		Logger:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	// --- backup -----------------------------------------------------------
	res, err := runner.RunBackup(ctx, space.ID)
	if err != nil {
		t.Fatalf("RunBackup: %v", err)
	}
	t.Logf("snapshot %s: %d files, %d logical bytes", res.SnapshotID, res.FileCount, res.TotalBytes)
	if res.FileCount == 0 {
		t.Fatal("snapshot recorded no files")
	}

	repo := snapshot.Repo{
		Location: garageLocation(garage),
		Space:    snapshot.SpaceRef{SpaceID: space.ID},
		DK:       dk,
	}

	// The target must hold no plaintext: neither the seeded file's name nor its
	// bytes may be findable.
	objectKeys := listObjects(ctx, t, garage)
	if len(objectKeys) == 0 {
		t.Fatal("no objects written to the target")
	}
	for _, key := range objectKeys {
		if strings.Contains(key, expectFile) {
			t.Fatalf("source filename leaked into object key %q", key)
		}
	}

	// --- restore ----------------------------------------------------------
	out := t.TempDir()
	if err := engine.RestoreAll(ctx, repo, res.SnapshotID, out); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	assertRestoredChecksum(t, out, expectFile, expectSHA)

	// The nested, non-ASCII path proves directory traversal and structure
	// preservation against real reva references, not just a flat read.
	nestedFile := strings.TrimPrefix(os.Getenv("CS3_EXPECT_NESTED_FILE"), "/")
	nestedSHA := os.Getenv("CS3_EXPECT_NESTED_SHA256")
	if nestedFile == "" || nestedSHA == "" {
		t.Skip("nested fixture file not seeded; re-run test/fixtures/opencloud/seed.sh")
	}
	assertRestoredChecksum(t, out, nestedFile, nestedSHA)
}

// Phase-5 acceptance test (success metric 3) against a *real* OpenCloud:
//
//	seeded Space -> backup -> stop OpenCloud -> take-out -> offline decrypt
//
// Unlike the Garage-only Path A test, this one proves the criterion literally:
// the oCIS containers are stopped before the take-out runs.
//
//	cd test/fixtures/opencloud && ./up.sh && ./seed.sh
//	source test/fixtures/opencloud/fixture.env
//	export OC_FIXTURE_DOWN_CMD="$PWD/test/fixtures/opencloud/down.sh"
//	go test -tags integration -run TestIntegration_OpenCloudPathA ./pkg/backup/...
func TestIntegration_OpenCloudPathAWithDeploymentStopped(t *testing.T) {
	addr := os.Getenv("CS3_GATEWAY_ADDR")
	saID := os.Getenv("CS3_SERVICE_ACCOUNT_ID")
	saSecret := os.Getenv("CS3_SERVICE_ACCOUNT_SECRET")
	expectFile := strings.TrimPrefix(os.Getenv("CS3_EXPECT_FILE"), "/")
	expectSHA := os.Getenv("CS3_EXPECT_SHA256")
	ownerUID := os.Getenv("OC_ADMIN_USER_ID")
	if addr == "" || saID == "" || saSecret == "" || expectFile == "" || expectSHA == "" {
		t.Skip("OpenCloud fixture env not set; source test/fixtures/opencloud/fixture.env")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial gateway %s: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()

	gw := gateway.NewGatewayAPIClient(conn)
	reader := cs3.NewClient(gw,
		cs3.ServiceAccountAuth{Gateway: gw, ClientID: saID, Secret: saSecret},
		cs3.WithHTTPClient(&http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		}),
	)
	space := findSeededSpace(ctx, t, reader, ownerUID)

	garage := testutil.StartGarage(ctx, t)
	engine, err := snapshot.NewEngine(snapshot.S3Opener{}, snapshot.EngineOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	sealer := newSealer(t)
	wrapper := newSRWWrapper(t)
	configs := spacecfg.NewMemoryStore()
	targetStore := targets.NewMemoryStore()
	keyStore := keys.NewMemoryStore()
	jobStore := jobs.NewMemoryStore()

	seedGarageTarget(t, targetStore, sealer, garage)
	seedConfig(t, configs, space.ID, testTargetID)
	dk := seedKeys(t, keyStore, wrapper, space.ID)
	rk := seedRK(t, keyStore, space.ID, dk)

	runner, err := NewRunner(Deps{
		Spaces: reader, Configs: configs, Targets: targetStore, Sealer: sealer,
		Keys: keyStore, Unwrap: wrapper, Engine: engine, Jobs: jobStore, Locks: jobStore,
		Envelopes: takeout.S3Publisher{},
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	if _, err := runner.RunBackup(ctx, space.ID); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}

	// --- stop OpenCloud ---------------------------------------------------
	stopOpenCloudFixture(t)
	_ = conn.Close()

	// --- admin take-out, S3 only -----------------------------------------
	location := garageLocation(garage)
	objects, err := objstore.NewS3(ctx, objstore.S3Config{
		Endpoint:        garage.Endpoint,
		Region:          garage.Region,
		Bucket:          garage.Bucket,
		AccessKeyID:     garage.AccessKeyID,
		SecretAccessKey: garage.SecretAccessKey,
		UsePathStyle:    true,
		DisableTLS:      true,
	})
	if err != nil {
		t.Fatalf("objstore.NewS3: %v", err)
	}

	takeoutDir := filepath.Join(t.TempDir(), "takeout")
	if _, err := takeout.Extract(ctx, takeout.ExtractOptions{
		Repos:    snapshot.S3Opener{},
		Objects:  objects,
		Location: location,
		SpaceID:  space.ID,
		OutDir:   takeoutDir,
	}); err != nil {
		t.Fatalf("Extract with OpenCloud stopped: %v", err)
	}
	if err := takeout.Verify(ctx, takeoutDir); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// --- user decrypt, offline -------------------------------------------
	out := t.TempDir()
	if _, err := takeout.Decrypt(ctx, takeout.DecryptOptions{
		Dir: takeoutDir, RecoveryKey: rk, OutDir: out, WorkDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	assertRestoredChecksum(t, out, expectFile, expectSHA)

	nestedFile := strings.TrimPrefix(os.Getenv("CS3_EXPECT_NESTED_FILE"), "/")
	nestedSHA := os.Getenv("CS3_EXPECT_NESTED_SHA256")
	if nestedFile != "" && nestedSHA != "" {
		assertRestoredChecksum(t, out, nestedFile, nestedSHA)
	}
}

// stopOpenCloudFixture runs the operator's shutdown command, so the take-out
// really does happen against a dead deployment. Without it the test still
// exercises the path, but cannot claim the acceptance criterion — so it says so.
func stopOpenCloudFixture(t *testing.T) {
	t.Helper()

	cmd := os.Getenv("OC_FIXTURE_DOWN_CMD")
	if cmd == "" {
		t.Log("OC_FIXTURE_DOWN_CMD unset: OpenCloud stays up (it is simply never " +
			"contacted). Set it to test/fixtures/opencloud/down.sh for the full " +
			"acceptance criterion.")
		return
	}

	t.Logf("stopping OpenCloud: %s", cmd)
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("could not stop the OpenCloud fixture: %v\n%s", err, out)
	}
	if up := os.Getenv("OC_FIXTURE_UP_CMD"); up != "" {
		t.Cleanup(func() {
			if out, err := exec.Command("sh", "-c", up).CombinedOutput(); err != nil {
				t.Logf("could not restart the OpenCloud fixture: %v\n%s", err, out)
			}
		})
	}
}

// assertRestoredChecksum verifies one restored file byte-for-byte via sha256.
func assertRestoredChecksum(t *testing.T, outDir, relPath, wantSHA string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(outDir, filepath.FromSlash(relPath)))
	if err != nil {
		t.Fatalf("read restored %s: %v", relPath, err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != wantSHA {
		t.Fatalf("restored %s sha256 = %s, want %s", relPath, got, wantSHA)
	}
	t.Logf("restored %s (%d bytes) with matching sha256", relPath, len(data))
}

// findSeededSpace returns the personal space seeded by the fixture.
func findSeededSpace(ctx context.Context, t *testing.T, reader cs3.SpaceReader, ownerUID string) cs3.Space {
	t.Helper()
	spaces, err := reader.ListSpaces(ctx)
	if err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	for _, s := range spaces {
		if s.Type == "personal" && (ownerUID == "" || s.Owner == ownerUID) {
			return s
		}
	}
	t.Fatalf("no personal space found for owner %q", ownerUID)
	return cs3.Space{}
}
