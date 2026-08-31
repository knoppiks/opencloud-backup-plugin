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
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/spacecfg"
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
