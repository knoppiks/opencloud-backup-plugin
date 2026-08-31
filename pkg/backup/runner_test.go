package backup

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/targets"
)

var epoch = time.Date(2026, 7, 8, 9, 10, 11, 0, time.UTC)

// harness wires a Runner over in-memory stores and a fake engine.
type harness struct {
	runner  *Runner
	reader  *fakeReader
	engine  *fakeEngine
	configs *spacecfg.MemoryStore
	targets *targets.MemoryStore
	keys    *keys.MemoryStore
	jobs    *jobs.MemoryStore
	clock   *testutil.FakeClock
	dk      []byte
	logs    *bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	space := cs3.Space{
		ID:    testSpaceID,
		Name:  "Alice",
		Type:  "personal",
		Owner: "alice",
		Root:  cs3.ResourceID{StorageID: "storage-1", SpaceID: "space-1", OpaqueID: "root-1"},
	}
	reader := newFakeReader(space)
	reader.put("readme.txt", []byte("hello"), testMTime)
	reader.put("docs/notes.txt", []byte("notes body"), testMTime)

	sealer := newSealer(t)
	wrapper := newSRWWrapper(t)

	h := &harness{
		reader:  reader,
		engine:  &fakeEngine{info: snapshot.Info{ID: "snap-1", FileCount: 2, TotalBytes: 15}},
		configs: spacecfg.NewMemoryStore(),
		targets: targets.NewMemoryStore(),
		keys:    keys.NewMemoryStore(),
		clock:   testutil.NewFakeClock(epoch),
		logs:    &bytes.Buffer{},
	}
	h.jobs = jobs.NewMemoryStoreWithClock(h.clock)

	seedTarget(t, h.targets, sealer)
	seedConfig(t, h.configs, testSpaceID, testTargetID)
	h.dk = seedKeys(t, h.keys, wrapper, testSpaceID)

	runner, err := NewRunner(Deps{
		Spaces:  reader,
		Configs: h.configs,
		Targets: h.targets,
		Sealer:  sealer,
		Keys:    h.keys,
		Unwrap:  wrapper,
		Engine:  h.engine,
		Jobs:    h.jobs,
		Locks:   h.jobs,
		Logger:  slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Clock:   h.clock,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	h.runner = runner
	return h
}

func TestNewRunner_RequiresEveryDependency(t *testing.T) {
	if _, err := NewRunner(Deps{}); err == nil {
		t.Fatal("empty deps must be rejected")
	}
}

func TestRunBackup_HappyPath(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	res, err := h.runner.RunBackup(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("RunBackup: %v", err)
	}
	if res.SnapshotID != "snap-1" || res.SpaceID != testSpaceID {
		t.Fatalf("result = %+v", res)
	}
	if res.FileCount != 2 || res.TotalBytes != 15 {
		t.Fatalf("stats not propagated: %+v", res)
	}

	job, err := h.jobs.Get(ctx, res.JobID)
	if err != nil {
		t.Fatalf("Get job: %v", err)
	}
	if job.State != jobs.StateSucceeded {
		t.Fatalf("job state = %q, want succeeded", job.State)
	}
	if job.SnapshotID != "snap-1" || job.Kind != jobs.KindBackup {
		t.Fatalf("job = %+v", job)
	}
	if job.Error != "" {
		t.Fatalf("successful job carries error %q", job.Error)
	}
}

// The engine must receive the Space's real Data Key and the target's
// TW-unwrapped credentials — resolved server-side, never from client input.
func TestRunBackup_ResolvesKeyAndTargetCredentials(t *testing.T) {
	h := newHarness(t)

	if _, err := h.runner.RunBackup(context.Background(), testSpaceID); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}

	call := h.engine.lastCall(t)
	if !bytes.Equal(call.DK, h.dk) {
		t.Fatal("engine did not receive the space's data key")
	}
	if call.Space.SpaceID != testSpaceID {
		t.Fatalf("engine space = %q", call.Space.SpaceID)
	}
	loc := call.Location
	if loc.AccessKeyID != "GK-test-access-key" ||
		loc.SecretAccessKey != "test-secret-access-key-0000000000000000" {
		t.Fatal("target credentials were not TW-unwrapped into the location")
	}
	if loc.Bucket != "backups" || loc.Prefix != "oc/" || loc.Endpoint != "garage:3900" || !loc.DisableTLS {
		t.Fatalf("target addressing not propagated: %+v", loc.Redacted())
	}
}

// The plaintext Data Key must not outlive the run.
func TestRunBackup_ZeroizesDataKeyAfterRun(t *testing.T) {
	h := newHarness(t)

	var liveDK []byte
	h.engine.onSnapshot = func(r snapshot.Repo) { liveDK = r.DK }

	if _, err := h.runner.RunBackup(context.Background(), testSpaceID); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}
	if liveDK == nil {
		t.Fatal("engine never saw a data key")
	}
	for _, b := range liveDK {
		if b != 0 {
			t.Fatal("data key buffer was not zeroized after the run")
		}
	}
}

func TestRunBackup_SourceStreamsFromCS3(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	if _, err := h.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}

	src := h.engine.sources[0]
	if src.Name() != testSpaceID {
		t.Fatalf("source name = %q, want the stable space id", src.Name())
	}

	root, err := src.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(root) != 2 {
		t.Fatalf("root children = %+v, want docs/ and readme.txt", root)
	}
	byName := map[string]snapshot.Node{}
	for _, n := range root {
		byName[n.Name] = n
	}
	if !byName["docs"].IsDir {
		t.Fatalf("docs must be a directory: %+v", byName["docs"])
	}
	file := byName["readme.txt"]
	if file.IsDir || file.Size != 5 || !file.ModTime.Equal(testMTime) {
		t.Fatalf("readme.txt node = %+v", file)
	}

	rc, err := src.Open(ctx, "readme.txt", 1)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rc.Close() }()
	buf := make([]byte, 4)
	if _, err := rc.Read(buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf) != "ello" {
		t.Fatalf("offset read = %q, want ello", buf)
	}
}

func TestRunBackup_UnknownSpace(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	if _, err := h.runner.RunBackup(ctx, "no-such-space"); !errors.Is(err, ErrSpaceNotFound) {
		t.Fatalf("error = %v, want ErrSpaceNotFound", err)
	}
	if _, err := h.runner.RunBackup(ctx, ""); !errors.Is(err, ErrSpaceNotFound) {
		t.Fatalf("empty space id error = %v, want ErrSpaceNotFound", err)
	}
	// No job record is created for a Space that does not exist.
	if got, _ := h.jobs.List(ctx, "no-such-space"); len(got) != 0 {
		t.Fatalf("unknown space produced %d job records", len(got))
	}
}

func TestRunBackup_NotConfigured(t *testing.T) {
	ctx := context.Background()

	t.Run("no space configuration", func(t *testing.T) {
		h := newHarness(t)
		if err := h.configs.Delete(ctx, testSpaceID); err != nil {
			t.Fatalf("Delete config: %v", err)
		}
		assertFailedRun(t, h, ErrNotConfigured, "backup is not configured for this space")
	})

	t.Run("no key envelope", func(t *testing.T) {
		h := newHarness(t)
		h.keys.Delete(testSpaceID)
		assertFailedRun(t, h, ErrNotConfigured, "backup is not configured for this space")
	})
}

func TestRunBackup_TargetUnavailable(t *testing.T) {
	ctx := context.Background()

	t.Run("target deleted", func(t *testing.T) {
		h := newHarness(t)
		if err := h.targets.DeleteTarget(ctx, testTargetID); err != nil {
			t.Fatalf("DeleteTarget: %v", err)
		}
		assertFailedRun(t, h, ErrTargetUnavailable, "the backup target is unavailable")
	})

	t.Run("credentials sealed with another key", func(t *testing.T) {
		h := newHarness(t)
		// Re-seal with a different TW key: the run must fail closed.
		other := newSealer(t)
		wrapped, version, err := other.Seal(targets.PlainCreds{AccessKeyID: "a", SecretAccessKey: "b"})
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		target, _ := h.targets.GetTarget(ctx, testTargetID)
		target.WrappedCreds, target.Version = wrapped, version
		if _, err := h.targets.UpdateTarget(ctx, target); err != nil {
			t.Fatalf("UpdateTarget: %v", err)
		}
		assertFailedRun(t, h, ErrTargetUnavailable, "the backup target is unavailable")
	})
}

func TestRunBackup_EngineFailureIsRecordedAndSanitized(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.engine.err = errors.New("s3: connection refused to garage.internal:3900")

	_, err := h.runner.RunBackup(ctx, testSpaceID)
	if !errors.Is(err, ErrRunFailed) {
		t.Fatalf("error = %v, want ErrRunFailed", err)
	}

	list, err := h.jobs.List(ctx, testSpaceID)
	if err != nil || len(list) != 1 {
		t.Fatalf("List jobs = %v, %v", list, err)
	}
	job := list[0]
	if job.State != jobs.StateFailed {
		t.Fatalf("job state = %q, want failed", job.State)
	}
	if job.Error != "the backup run failed" {
		t.Fatalf("job error = %q, want a sanitized message", job.Error)
	}
	if bytes.Contains([]byte(job.Error), []byte("garage.internal")) {
		t.Fatal("job error leaked internal target detail")
	}
}

// Concurrent runs for the same Space are prevented; a different Space is not.
func TestRunBackup_LockExcludesConcurrentRuns(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	release, err := h.jobs.Acquire(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, err := h.runner.RunBackup(ctx, testSpaceID); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("error = %v, want ErrRunInProgress", err)
	}
	if h.engine.callCount() != 0 {
		t.Fatal("a locked space must not reach the engine")
	}
	release()

	if _, err := h.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("RunBackup after release: %v", err)
	}
}

func TestRunBackup_ReleasesLockOnFailure(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.engine.err = errors.New("boom")

	if _, err := h.runner.RunBackup(ctx, testSpaceID); err == nil {
		t.Fatal("expected failure")
	}
	// The lock must be free again, or a Space could never be retried.
	release, err := h.jobs.Acquire(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("lock still held after failed run: %v", err)
	}
	release()
}

// Logs are operational: never key material, never credentials.
func TestRunBackup_LogsNoSecrets(t *testing.T) {
	h := newHarness(t)
	h.engine.err = errors.New("upstream failure")

	if _, err := h.runner.RunBackup(context.Background(), testSpaceID); err == nil {
		t.Fatal("expected failure")
	}
	// Also exercise the success path's logging.
	h.engine.err = nil
	if _, err := h.runner.RunBackup(context.Background(), testSpaceID); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}

	logged := h.logs.String()
	for _, secret := range []string{
		"GK-test-access-key",
		"test-secret-access-key-0000000000000000",
	} {
		if bytes.Contains([]byte(logged), []byte(secret)) {
			t.Fatalf("logs leaked %q", secret)
		}
	}
	if bytes.Contains([]byte(logged), h.dk) {
		t.Fatal("logs leaked raw data key bytes")
	}
}

func TestRunBackup_ListSpacesFailurePropagates(t *testing.T) {
	h := newHarness(t)
	h.reader.listSpacesErr = errors.New("gateway down")

	if _, err := h.runner.RunBackup(context.Background(), testSpaceID); err == nil {
		t.Fatal("expected the upstream failure to propagate")
	}
}

func TestStartBackup_RunsInBackgroundAndOutlivesRequestContext(t *testing.T) {
	h := newHarness(t)

	// The request context is cancelled as soon as the handler returns; the run
	// must still complete.
	reqCtx, cancel := context.WithCancel(context.Background())

	started := make(chan struct{})
	proceed := make(chan struct{})
	h.engine.onSnapshot = func(snapshot.Repo) {
		close(started)
		<-proceed
	}

	jobID, err := h.runner.StartBackup(reqCtx, testSpaceID)
	if err != nil {
		t.Fatalf("StartBackup: %v", err)
	}
	if jobID == "" {
		t.Fatal("StartBackup must return a job id")
	}
	cancel()

	<-started
	close(proceed)

	job := waitForState(t, h, jobID, jobs.StateSucceeded)
	if job.SnapshotID != "snap-1" {
		t.Fatalf("job = %+v", job)
	}
}

func TestStartBackup_FailsFastOnUnknownSpaceAndLock(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	if _, err := h.runner.StartBackup(ctx, "nope"); !errors.Is(err, ErrSpaceNotFound) {
		t.Fatalf("error = %v, want ErrSpaceNotFound", err)
	}

	release, err := h.jobs.Acquire(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()
	if _, err := h.runner.StartBackup(ctx, testSpaceID); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("error = %v, want ErrRunInProgress", err)
	}
}

func TestStartBackup_RecordsFailureAndReleasesLock(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.engine.err = errors.New("boom")

	jobID, err := h.runner.StartBackup(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("StartBackup: %v", err)
	}
	job := waitForState(t, h, jobID, jobs.StateFailed)
	if job.Error != "the backup run failed" {
		t.Fatalf("job error = %q", job.Error)
	}

	release, err := h.jobs.Acquire(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("lock still held after background run: %v", err)
	}
	release()
}

// waitForState polls the job store until the job reaches want or the test times
// out. Polling keeps the runner free of test-only synchronisation hooks.
func waitForState(t *testing.T, h *harness, jobID string, want jobs.State) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := h.jobs.Get(context.Background(), jobID)
		if err != nil {
			t.Fatalf("Get job: %v", err)
		}
		if job.State == want {
			return job
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("job %s did not reach state %q in time", jobID, want)
	return jobs.Job{}
}

func TestUserMessage(t *testing.T) {
	cases := map[error]string{
		ErrNotConfigured:       "backup is not configured for this space",
		ErrTargetUnavailable:   "the backup target is unavailable",
		ErrSpaceNotFound:       "space not found",
		ErrRunInProgress:       "a run is already in progress",
		errors.New("internal"): "the backup run failed",
	}
	for err, want := range cases {
		if got := userMessage(err); got != want {
			t.Fatalf("userMessage(%v) = %q, want %q", err, got, want)
		}
	}
}

// assertFailedRun runs a backup expecting it to fail with want, and checks the
// job record carries the sanitized message.
func assertFailedRun(t *testing.T, h *harness, want error, wantMsg string) {
	t.Helper()
	ctx := context.Background()

	_, err := h.runner.RunBackup(ctx, testSpaceID)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	list, err := h.jobs.List(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("List jobs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 job record, got %d", len(list))
	}
	if list[0].State != jobs.StateFailed {
		t.Fatalf("job state = %q, want failed", list[0].State)
	}
	if list[0].Error != wantMsg {
		t.Fatalf("job error = %q, want %q", list[0].Error, wantMsg)
	}
	if h.engine.callCount() != 0 {
		t.Fatal("engine must not run when resolution failed")
	}
}
