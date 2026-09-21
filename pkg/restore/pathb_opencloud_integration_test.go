//go:build integration

package restore_test

// Restore Path B against a real OpenCloud, not a stand-in (remediation R9).
//
// The sibling suite in pathb_integration_test.go drives the same code against an
// in-memory Space. That proves the orchestration and proves nothing about reva:
// the fake was written to agree with the code, so the questions that actually
// decide whether a family gets its files back — does a modification time survive
// an upload, what happens to an empty file, an empty folder, a name with spaces
// or non-Latin characters — were being answered by our own assumptions.
//
// This suite seeds those cases into a live Space, backs it up to an ephemeral
// Garage, restores into Restore/<timestamp>/, and reads the result back out
// through CS3.
//
//	cd test/fixtures/opencloud && ./up.sh && ./seed.sh
//	source test/fixtures/opencloud/fixture.env
//	go test -tags integration -run OpenCloud ./pkg/restore/...

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"testing"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/backup"
	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/restore"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/targets"
)

// sourceMTime is deliberately old and deliberately not on a minute boundary:
// a server that quietly substitutes "now" is then obvious, and one that
// truncates is visible in the seconds.
var sourceMTime = time.Date(2019, 11, 3, 14, 27, 53, 0, time.UTC)

// seededFile is one case the restore has to reproduce exactly.
type seededFile struct {
	// why records what this case exists to catch, so a failure names it.
	why     string
	relPath string
	content []byte
}

// seededFiles are the awkward shapes. The ordinary file is here too: without it
// a wholesale failure would look like a collection of edge-case failures.
func seededFiles() []seededFile {
	return []seededFile{
		{why: "an ordinary file", relPath: "readme.txt", content: []byte("hello from a real space")},
		{why: "a zero-byte file", relPath: "empty.txt", content: []byte{}},
		{why: "spaces in the name", relPath: "a file with spaces.txt", content: []byte("spaced out")},
		{why: "non-Latin characters", relPath: "фото/café.txt", content: []byte("unicode content")},
		{why: "a nested path", relPath: "docs/notes/deep.txt", content: []byte("nested notes")},
	}
}

// emptyDirName is the directory that holds nothing. kopia records it and the
// restore has to create it: a folder that vanishes is data loss the user only
// notices later.
const emptyDirName = "an empty folder"

func TestIntegration_PathB_OpenCloudRoundTrip(t *testing.T) {
	env := testutil.OpenCloudEnv(t,
		"CS3_GATEWAY_ADDR", "CS3_SERVICE_ACCOUNT_ID", "CS3_SERVICE_ACCOUNT_SECRET")
	addr, saID, saSecret := env[0], env[1], env[2]

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	client := openCloudClient(ctx, t, addr, saID, saSecret)
	space := writableSpace(ctx, t, client)
	t.Logf("using space id=%s name=%q", space.ID, space.Name)

	// Everything this test writes lives under one folder, removed afterwards,
	// so a long-lived fixture does not silently accumulate test data.
	sourceDir := fmt.Sprintf("pathb-%d", time.Now().UnixNano())
	seedSpace(ctx, t, client, space, sourceDir)

	// --- back the Space up to a real target -------------------------------
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

	seedTarget(t, targetStore, sealer, snapshot.Location{
		Endpoint:        strings.TrimPrefix(garage.Endpoint, "http://"),
		Region:          garage.Region,
		Bucket:          garage.Bucket,
		Prefix:          prefix,
		AccessKeyID:     garage.AccessKeyID,
		SecretAccessKey: garage.SecretAccessKey,
		DisableTLS:      true,
	})
	seedConfig(t, configs, space.ID)
	seedKeys(t, keyStore, wrapper, space.ID)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	deps := func() (*backup.Runner, *restore.Runner) {
		b, err := backup.NewRunner(backup.Deps{
			Spaces: client, Configs: configs, Targets: targetStore, Sealer: sealer,
			Keys: keyStore, Unwrap: wrapper, Engine: engine, Jobs: jobStore, Locks: jobStore,
			Logger: logger,
		})
		if err != nil {
			t.Fatalf("backup.NewRunner: %v", err)
		}
		r, err := restore.NewRunner(restore.Deps{
			Spaces: client, Writer: client, Configs: configs, Targets: targetStore, Sealer: sealer,
			Keys: keyStore, Unwrap: wrapper, Engine: engine, Jobs: jobStore, Locks: jobStore,
			Logger: logger,
		})
		if err != nil {
			t.Fatalf("restore.NewRunner: %v", err)
		}
		return b, r
	}
	backupRunner, restoreRunner := deps()

	res, err := backupRunner.RunBackup(ctx, space.ID)
	if err != nil {
		t.Fatalf("RunBackup: %v", err)
	}
	t.Logf("snapshot %s: %d files, %d bytes", res.SnapshotID, res.FileCount, res.TotalBytes)

	// --- restore it back into the same Space ------------------------------
	restored, err := restoreRunner.RunRestore(ctx, space.ID, res.SnapshotID)
	if err != nil {
		t.Fatalf("RunRestore: %v", err)
	}
	t.Cleanup(func() { removeTree(t, client, space, restored.Folder) })

	if !strings.HasPrefix(restored.Folder, restore.RestoreFolder+"/") {
		t.Fatalf("restore folder = %q, want a child of %s/", restored.Folder, restore.RestoreFolder)
	}

	// --- what came back ---------------------------------------------------
	base := path.Join(restored.Folder, sourceDir)

	for _, f := range seededFiles() {
		got := readFile(ctx, t, client, space, path.Join(base, f.relPath))
		if !bytes.Equal(got, f.content) {
			t.Errorf("%s (%s): content = %q, want %q", f.relPath, f.why, got, f.content)
		}
	}

	// An empty directory is not implied by any file, so nothing else here
	// would notice it going missing.
	if !dirExists(ctx, t, client, space, path.Join(base, emptyDirName)) {
		t.Errorf("the empty directory %q was not restored", emptyDirName)
	}

	// Names survive the round trip through kopia and back into reva. A name
	// that changes is a file the user cannot find.
	assertNamesPreserved(ctx, t, client, space, base)

	// Modification time is in backup scope (decisions.md #4), and the upload
	// asks for it with X-OC-Mtime. What reva does with that is pinned here.
	assertMTimePreserved(ctx, t, client, space, path.Join(base, "readme.txt"))
}

// The restore must never touch what is already in the Space: it writes into a
// fresh folder and nothing else (decisions.md #3).
func TestIntegration_PathB_OpenCloudLeavesLiveDataAlone(t *testing.T) {
	env := testutil.OpenCloudEnv(t,
		"CS3_GATEWAY_ADDR", "CS3_SERVICE_ACCOUNT_ID", "CS3_SERVICE_ACCOUNT_SECRET")
	addr, saID, saSecret := env[0], env[1], env[2]

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	client := openCloudClient(ctx, t, addr, saID, saSecret)
	space := writableSpace(ctx, t, client)

	sourceDir := fmt.Sprintf("pathb-live-%d", time.Now().UnixNano())
	seedSpace(ctx, t, client, space, sourceDir)

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

	seedTarget(t, targetStore, sealer, snapshot.Location{
		Endpoint:        strings.TrimPrefix(garage.Endpoint, "http://"),
		Region:          garage.Region,
		Bucket:          garage.Bucket,
		Prefix:          prefix,
		AccessKeyID:     garage.AccessKeyID,
		SecretAccessKey: garage.SecretAccessKey,
		DisableTLS:      true,
	})
	seedConfig(t, configs, space.ID)
	seedKeys(t, keyStore, wrapper, space.ID)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	backupRunner, err := backup.NewRunner(backup.Deps{
		Spaces: client, Configs: configs, Targets: targetStore, Sealer: sealer,
		Keys: keyStore, Unwrap: wrapper, Engine: engine, Jobs: jobStore, Locks: jobStore,
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("backup.NewRunner: %v", err)
	}
	restoreRunner, err := restore.NewRunner(restore.Deps{
		Spaces: client, Writer: client, Configs: configs, Targets: targetStore, Sealer: sealer,
		Keys: keyStore, Unwrap: wrapper, Engine: engine, Jobs: jobStore, Locks: jobStore,
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("restore.NewRunner: %v", err)
	}

	res, err := backupRunner.RunBackup(ctx, space.ID)
	if err != nil {
		t.Fatalf("RunBackup: %v", err)
	}

	// Change a live file after the snapshot. If a restore overwrote, this is
	// the edit that would disappear.
	edited := []byte("edited after the snapshot was taken")
	upload(ctx, t, client, space, path.Join(sourceDir, "readme.txt"), edited)

	restored, err := restoreRunner.RunRestore(ctx, space.ID, res.SnapshotID)
	if err != nil {
		t.Fatalf("RunRestore: %v", err)
	}
	t.Cleanup(func() { removeTree(t, client, space, restored.Folder) })

	if got := readFile(ctx, t, client, space, path.Join(sourceDir, "readme.txt")); !bytes.Equal(got, edited) {
		t.Fatalf("the restore overwrote live data: %q", got)
	}
	if got := readFile(ctx, t, client, space, path.Join(restored.Folder, sourceDir, "readme.txt")); !bytes.Equal(got, []byte("hello from a real space")) {
		t.Fatalf("restored copy = %q, want the snapshot's content", got)
	}
}

// --- fixture helpers --------------------------------------------------------

func openCloudClient(_ context.Context, t *testing.T, addr, saID, saSecret string) *cs3.Client {
	t.Helper()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial gateway %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	gw := gateway.NewGatewayAPIClient(conn)
	return cs3.NewClient(gw,
		cs3.ServiceAccountAuth{Gateway: gw, ClientID: saID, Secret: saSecret},
		// The fixture serves the data gateway with a self-signed certificate.
		cs3.WithHTTPClient(&http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		}),
	)
}

// writableSpace picks the Space to round-trip through: the seeded project Space
// when seed.sh made one, otherwise whatever the service account can see. The
// project Space is preferred because it is the shape a family actually shares.
func writableSpace(ctx context.Context, t *testing.T, client *cs3.Client) cs3.Space {
	t.Helper()

	spaces, err := client.ListSpaces(ctx)
	if err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	if len(spaces) == 0 {
		testutil.FixtureGap(t, "no space visible to the service account")
	}

	if shared := strings.TrimSpace(os.Getenv("OC_SHARED_SPACE_ID")); shared != "" {
		for _, s := range spaces {
			// The graph drive id is a prefix of the CS3 space id.
			if strings.HasPrefix(s.ID, shared) {
				return s
			}
		}
	}
	return spaces[0]
}

// seedSpace writes the awkward shapes into the Space under dir.
func seedSpace(ctx context.Context, t *testing.T, client *cs3.Client, space cs3.Space, dir string) {
	t.Helper()

	if err := client.MakeDir(ctx, space, dir); err != nil {
		t.Fatalf("MakeDir %s: %v", dir, err)
	}
	t.Cleanup(func() { removeTree(t, client, space, dir) })

	if err := client.MakeDir(ctx, space, path.Join(dir, emptyDirName)); err != nil {
		t.Fatalf("MakeDir %s: %v", emptyDirName, err)
	}

	for _, f := range seededFiles() {
		if parent := path.Dir(f.relPath); parent != "." {
			for _, segment := range parents(path.Join(dir, parent)) {
				if err := client.MakeDir(ctx, space, segment); err != nil {
					t.Fatalf("MakeDir %s: %v", segment, err)
				}
			}
		}
		upload(ctx, t, client, space, path.Join(dir, f.relPath), f.content)
	}
}

// parents lists a path's ancestors from the top down, so each can be created in
// turn: reva has no mkdir -p.
func parents(p string) []string {
	segments := strings.Split(p, "/")
	out := make([]string, 0, len(segments))
	for i := range segments {
		out = append(out, strings.Join(segments[:i+1], "/"))
	}
	return out
}

func upload(ctx context.Context, t *testing.T, client *cs3.Client, space cs3.Space, relPath string, content []byte) {
	t.Helper()
	if err := client.Upload(ctx, space, relPath, int64(len(content)), sourceMTime, bytes.NewReader(content)); err != nil {
		t.Fatalf("Upload %s: %v", relPath, err)
	}
}

func readFile(ctx context.Context, t *testing.T, client *cs3.Client, space cs3.Space, relPath string) []byte {
	t.Helper()

	rc, err := client.OpenFile(ctx, space, relPath, 0)
	if err != nil {
		t.Fatalf("OpenFile %s: %v", relPath, err)
	}
	defer func() { _ = rc.Close() }()

	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", relPath, err)
	}
	return body
}

func dirExists(ctx context.Context, t *testing.T, client *cs3.Client, space cs3.Space, relDir string) bool {
	t.Helper()

	parent, name := path.Split(relDir)
	entries, err := client.ListDir(ctx, space, strings.TrimSuffix(parent, "/"))
	if err != nil {
		t.Fatalf("ListDir %s: %v", parent, err)
	}
	for _, e := range entries {
		if path.Base(e.Path) == name {
			return e.IsDir
		}
	}
	return false
}

// assertNamesPreserved compares the restored directory listing with what was
// seeded, at the top level of the restored tree.
func assertNamesPreserved(ctx context.Context, t *testing.T, client *cs3.Client, space cs3.Space, base string) {
	t.Helper()

	entries, err := client.ListDir(ctx, space, base)
	if err != nil {
		t.Fatalf("ListDir %s: %v", base, err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, path.Base(e.Path))
	}
	sort.Strings(got)

	want := []string{emptyDirName, "a file with spaces.txt", "docs", "empty.txt", "readme.txt", "фото"}
	sort.Strings(want)

	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("restored names = %v, want %v", got, want)
	}
}

// assertMTimePreserved pins what reva does with the X-OC-Mtime header the
// upload sends. mtime is the only file metadata in backup scope, so "restored"
// meaning "restored with its date" is worth knowing rather than assuming.
func assertMTimePreserved(ctx context.Context, t *testing.T, client *cs3.Client, space cs3.Space, relPath string) {
	t.Helper()

	parent, name := path.Split(relPath)
	entries, err := client.ListDir(ctx, space, strings.TrimSuffix(parent, "/"))
	if err != nil {
		t.Fatalf("ListDir %s: %v", parent, err)
	}
	for _, e := range entries {
		if path.Base(e.Path) != name {
			continue
		}
		got := time.Unix(e.MTimeUnix, 0).UTC()
		if !got.Equal(sourceMTime) {
			t.Errorf("restored mtime of %s = %s, want %s (X-OC-Mtime was not honoured; "+
				"if this is the server's settled behaviour, record it as a limitation "+
				"rather than deleting the assertion)", name, got, sourceMTime)
		}
		return
	}
	t.Errorf("%s is missing from the restored listing", relPath)
}

// removeTree deletes a path the test created. Best effort: a fixture left a
// little dirty is not worth failing a passing test over, but it is worth saying.
func removeTree(t *testing.T, client *cs3.Client, space cs3.Space, relPath string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if err := client.Delete(ctx, space, relPath); err != nil {
		t.Logf("could not remove %s from the fixture: %v", relPath, err)
	}
}
