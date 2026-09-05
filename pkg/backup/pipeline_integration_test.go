//go:build integration

package backup

// Phase-4 integration suite: the full pipeline against a real S3 target
// (ephemeral Garage). It proves the properties the phase exit criteria name:
//
//   - the bucket holds only encrypted chunks and obfuscated paths,
//   - a second run after a one-file change uploads roughly only the delta,
//   - a snapshot restores byte-identically with mtime preserved,
//   - a file above the multipart threshold round-trips,
//   - concurrent runs for one Space are prevented,
//   - a target outage fails the run without corrupting the repository, and the
//     next run succeeds.
//
// Run: go test -tags integration ./pkg/backup/...

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/takeout"
	"opencloud-backup-plugin/pkg/targets"
)

// Plaintext markers that must never appear anywhere in the bucket.
var contentMarkers = [][]byte{
	[]byte("top-secret plaintext marker ALPHA"),
	[]byte("nested marker BRAVO with more text"),
	[]byte("große-datei"),
}

// Source names that must never appear in an object key.
var nameMarkers = []string{"große-datei", "notes.txt", "readme.txt", "café", "фото"}

const testPrefix = "oc/"

type garagePipeline struct {
	runner *Runner
	engine *snapshot.KopiaEngine
	reader *fakeReader
	jobs   *jobs.MemoryStore
	garage *testutil.Garage
	repo   snapshot.Repo
	// rk is the Space's raw Recovery Key, as the user would hold it.
	rk []byte
	// bigFile is the >multipart-threshold payload seeded into the Space.
	bigFile []byte
}

func newGaragePipeline(ctx context.Context, t *testing.T) *garagePipeline {
	t.Helper()

	garage := testutil.StartGarage(ctx, t)

	space := cs3.Space{
		ID:    testSpaceID,
		Name:  "Alice",
		Type:  "personal",
		Owner: "alice",
		Root:  cs3.ResourceID{StorageID: "storage-1", SpaceID: "space-1", OpaqueID: "root-1"},
	}
	reader := newFakeReader(space)
	reader.put("readme.txt", []byte("top-secret plaintext marker ALPHA"), testMTime)
	reader.put("docs/notes.txt", []byte("nested marker BRAVO with more text"), testMTime)
	reader.put("фото/café.txt", []byte("unicode path content"), testMTime)

	// 24 MiB of random data with an embedded marker: exercises multipart upload
	// and proves content is encrypted at rest.
	big := make([]byte, 24<<20)
	if _, err := rand.Read(big); err != nil {
		t.Fatalf("rand: %v", err)
	}
	copy(big[1000:], []byte("große-datei"))
	reader.put("assets/große-datei.bin", big, testMTime)

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
	seedConfig(t, configs, testSpaceID, testTargetID)
	dk := seedKeys(t, keyStore, wrapper, testSpaceID)
	rk := seedRK(t, keyStore, testSpaceID, dk)

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
		// The real publisher: every run leaves the RK-wrapped envelope on the
		// target, which is what makes a Take-Out self-contained.
		Envelopes: takeout.S3Publisher{},
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	return &garagePipeline{
		runner:  runner,
		engine:  engine,
		reader:  reader,
		jobs:    jobStore,
		garage:  garage,
		rk:      rk,
		bigFile: big,
		repo: snapshot.Repo{
			Location: garageLocation(garage),
			Space:    snapshot.SpaceRef{SpaceID: testSpaceID},
			DK:       dk,
		},
	}
}

func garageLocation(g *testutil.Garage) snapshot.Location {
	return snapshot.Location{
		Endpoint:        strings.TrimPrefix(g.Endpoint, "http://"),
		Region:          g.Region,
		Bucket:          g.Bucket,
		Prefix:          testPrefix,
		AccessKeyID:     g.AccessKeyID,
		SecretAccessKey: g.SecretAccessKey,
		DisableTLS:      true,
	}
}

// seedGarageTarget stores the Garage instance as a TW-sealed target.
func seedGarageTarget(t *testing.T, store *targets.MemoryStore, sealer targets.CredSealer, g *testutil.Garage) {
	t.Helper()
	wrapped, version, err := sealer.Seal(targets.PlainCreds{
		AccessKeyID:     g.AccessKeyID,
		SecretAccessKey: g.SecretAccessKey,
	})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := store.CreateTarget(context.Background(), targets.Target{
		ID:           testTargetID,
		Name:         "Garage",
		Endpoint:     strings.TrimPrefix(g.Endpoint, "http://"),
		Region:       g.Region,
		Bucket:       g.Bucket,
		Prefix:       testPrefix,
		UsePathStyle: true,
		DisableTLS:   true,
		WrappedCreds: wrapped,
		Version:      version,
	}); err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
}

func TestIntegration_BackupProducesEncryptedObfuscatedObjects(t *testing.T) {
	ctx := context.Background()
	p := newGaragePipeline(ctx, t)

	if _, err := p.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}

	objectKeys := listObjects(ctx, t, p.garage)
	if len(objectKeys) == 0 {
		t.Fatal("no objects written to the target")
	}

	// Every object belongs to this Space: either a repository blob, or the
	// Space's published recovery envelope. The envelope sits outside the repo
	// prefix on purpose — everything under it is kopia-owned (Phase 5).
	wantPrefix := snapshot.RepoPrefix(p.repo.Location, p.repo.Space)
	envelopeKey := snapshot.EnvelopeKey(p.repo.Location, p.repo.Space)
	sawEnvelope := false
	for _, key := range objectKeys {
		switch {
		case strings.HasPrefix(key, wantPrefix):
		case key == envelopeKey:
			sawEnvelope = true
		default:
			t.Fatalf("object %q belongs to neither the repo prefix %q nor the envelope %q",
				key, wantPrefix, envelopeKey)
		}
	}
	if !sawEnvelope {
		t.Fatalf("recovery envelope %q was not published to the target", envelopeKey)
	}

	assertNoPlaintext(ctx, t, p.garage, objectKeys)
}

func TestIntegration_SecondRunUploadsOnlyTheDelta(t *testing.T) {
	ctx := context.Background()
	p := newGaragePipeline(ctx, t)

	before := totalBytes(ctx, t, p.garage)
	if _, err := p.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("first RunBackup: %v", err)
	}
	afterFirst := totalBytes(ctx, t, p.garage)
	firstUpload := afterFirst - before

	// Change one small file; the 24 MiB binary is untouched.
	p.reader.put("docs/notes.txt", []byte("nested marker BRAVO with more text\none more line\n"), testMTime.Add(time.Hour))

	if _, err := p.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("second RunBackup: %v", err)
	}
	secondUpload := totalBytes(ctx, t, p.garage) - afterFirst

	t.Logf("uploaded: first=%d bytes second=%d bytes", firstUpload, secondUpload)
	if secondUpload >= firstUpload {
		t.Fatalf("expected dedup: second run uploaded %d bytes, first %d", secondUpload, firstUpload)
	}
	if secondUpload > 1<<20 {
		t.Fatalf("expected marginal upload after a one-line change, got %d bytes", secondUpload)
	}

	// The unchanged large file must not even be re-read from the Space.
	if got := p.reader.openCount("assets/große-datei.bin"); got != 1 {
		t.Fatalf("large unchanged file read %d times, want 1", got)
	}
}

func TestIntegration_RestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	p := newGaragePipeline(ctx, t)

	res, err := p.runner.RunBackup(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("RunBackup: %v", err)
	}

	out := t.TempDir()
	if err := p.engine.RestoreAll(ctx, p.repo, res.SnapshotID, out); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	for path, want := range map[string]string{
		"readme.txt":     "top-secret plaintext marker ALPHA",
		"docs/notes.txt": "nested marker BRAVO with more text",
		"фото/café.txt":  "unicode path content",
	} {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}

	// A file above the multipart threshold must round-trip byte-identically.
	gotBig, err := os.ReadFile(filepath.Join(out, "assets", "große-datei.bin"))
	if err != nil {
		t.Fatalf("read large file: %v", err)
	}
	if !bytes.Equal(gotBig, p.bigFile) {
		t.Fatalf("large file differs: %d vs %d bytes", len(gotBig), len(p.bigFile))
	}

	// mtime is in backup scope (decisions.md #4).
	fi, err := os.Stat(filepath.Join(out, "readme.txt"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !fi.ModTime().Truncate(time.Second).Equal(testMTime.Truncate(time.Second)) {
		t.Fatalf("mtime = %v, want %v", fi.ModTime(), testMTime)
	}
}

func TestIntegration_SingleFileRestore(t *testing.T) {
	ctx := context.Background()
	p := newGaragePipeline(ctx, t)

	res, err := p.runner.RunBackup(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("RunBackup: %v", err)
	}

	out := filepath.Join(t.TempDir(), "notes.txt")
	if err := p.engine.RestoreFile(ctx, p.repo, res.SnapshotID, "docs/notes.txt", out); err != nil {
		t.Fatalf("RestoreFile: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "nested marker BRAVO with more text" {
		t.Fatalf("restored = %q", got)
	}
}

func TestIntegration_ConcurrentRunsForOneSpaceArePrevented(t *testing.T) {
	ctx := context.Background()
	p := newGaragePipeline(ctx, t)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		ok       int
		rejected int
	)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.runner.RunBackup(ctx, testSpaceID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrRunInProgress):
				rejected++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if ok != 1 || rejected != 3 {
		t.Fatalf("concurrent runs: %d succeeded, %d rejected; want 1 and 3", ok, rejected)
	}
}

// A target that disappears mid-run must fail the run without leaving a repo the
// next run cannot use.
func TestIntegration_TargetOutageFailsRunAndRecovers(t *testing.T) {
	ctx := context.Background()
	p := newGaragePipeline(ctx, t)

	first, err := p.runner.RunBackup(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("first RunBackup: %v", err)
	}

	// Point the repo at an unreachable endpoint: the run must fail cleanly.
	broken := p.repo
	broken.Location.Endpoint = "127.0.0.1:1"
	if _, err := p.engine.Snapshot(ctx, broken, NewSpaceSource(p.reader, p.reader.space())); err == nil {
		t.Fatal("a snapshot to an unreachable target must fail")
	}

	// The repository is intact: the previous snapshot still restores and a new
	// run succeeds.
	out := t.TempDir()
	if err := p.engine.RestoreAll(ctx, p.repo, first.SnapshotID, out); err != nil {
		t.Fatalf("RestoreAll after outage: %v", err)
	}

	p.reader.put("readme.txt", []byte("top-secret plaintext marker ALPHA (edited)"), testMTime.Add(time.Hour))
	if _, err := p.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("RunBackup after outage: %v", err)
	}
	list, err := p.engine.List(ctx, p.repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 snapshots after recovery, got %d", len(list))
	}
}

// A run whose source fails must be recorded as failed and must not produce a
// snapshot claiming completeness.
func TestIntegration_SourceFailureIsRecorded(t *testing.T) {
	ctx := context.Background()
	p := newGaragePipeline(ctx, t)

	p.reader.failOpen("docs/notes.txt", fmt.Errorf("cs3 gone"))

	if _, err := p.runner.RunBackup(ctx, testSpaceID); !errors.Is(err, ErrRunFailed) {
		t.Fatalf("error = %v, want ErrRunFailed", err)
	}
	list, err := p.jobs.List(ctx, testSpaceID)
	if err != nil || len(list) != 1 {
		t.Fatalf("jobs = %v, %v", list, err)
	}
	if list[0].State != jobs.StateFailed {
		t.Fatalf("job state = %q, want failed", list[0].State)
	}

	snapshots, err := p.engine.List(ctx, p.repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snapshots) != 0 {
		t.Fatalf("a failed run persisted %d snapshots", len(snapshots))
	}

	// Recovery: the next run succeeds against the same repository.
	p.reader.failOpen("docs/notes.txt", nil)
	if _, err := p.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("RunBackup after recovery: %v", err)
	}
}

func TestIntegration_PruneKeepsWithinWindow(t *testing.T) {
	ctx := context.Background()
	p := newGaragePipeline(ctx, t)

	if _, err := p.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("RunBackup 1: %v", err)
	}
	p.reader.put("readme.txt", []byte("top-secret plaintext marker ALPHA (v2)"), testMTime.Add(time.Hour))
	second, err := p.runner.RunBackup(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("RunBackup 2: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	if err := p.engine.Prune(ctx, p.repo, time.Nanosecond); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	list, err := p.engine.List(ctx, p.repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != second.SnapshotID {
		t.Fatalf("after prune: %+v, want only the newest snapshot", list)
	}

	out := t.TempDir()
	if err := p.engine.RestoreAll(ctx, p.repo, second.SnapshotID, out); err != nil {
		t.Fatalf("RestoreAll after prune: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, "readme.txt"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "top-secret plaintext marker ALPHA (v2)" {
		t.Fatalf("restored = %q", got)
	}
}

// --- helpers ---------------------------------------------------------------

func listObjects(ctx context.Context, t *testing.T, g *testutil.Garage) []string {
	t.Helper()
	client := g.S3Client(ctx, t)

	var out []string
	pager := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(g.Bucket)})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			t.Fatalf("list objects: %v", err)
		}
		for _, o := range page.Contents {
			out = append(out, aws.ToString(o.Key))
		}
	}
	return out
}

func totalBytes(ctx context.Context, t *testing.T, g *testutil.Garage) int64 {
	t.Helper()
	client := g.S3Client(ctx, t)

	var total int64
	pager := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(g.Bucket)})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			t.Fatalf("list objects: %v", err)
		}
		for _, o := range page.Contents {
			total += aws.ToInt64(o.Size)
		}
	}
	return total
}

// assertNoPlaintext reads every object straight from Garage, bypassing kopia,
// and scans keys and bodies for source names and content markers.
func assertNoPlaintext(ctx context.Context, t *testing.T, g *testutil.Garage, objectKeys []string) {
	t.Helper()
	client := g.S3Client(ctx, t)

	for _, key := range objectKeys {
		for _, name := range nameMarkers {
			if strings.Contains(key, name) {
				t.Fatalf("source name %q leaked into object key %q", name, key)
			}
		}

		obj, err := client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(g.Bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			t.Fatalf("get object %s: %v", key, err)
		}
		body, err := io.ReadAll(obj.Body)
		_ = obj.Body.Close()
		if err != nil {
			t.Fatalf("read object %s: %v", key, err)
		}
		for _, marker := range contentMarkers {
			if bytes.Contains(body, marker) {
				t.Fatalf("plaintext marker %q leaked into object %s", marker, key)
			}
		}
	}
}
