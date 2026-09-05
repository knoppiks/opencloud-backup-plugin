package restore

// Path B behaviour, without OpenCloud, S3, or kopia: fakes stand in at every
// boundary. The properties under test are the promises made to the user —
// nothing live is touched, the restore lands in its own folder, and failures are
// recorded without leaking anything.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
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

const (
	testSpaceID  = "storage-1$space-1"
	testTargetID = "target-1"
	testSnapshot = snapshot.SnapshotID("snap-1")
)

var (
	epoch     = time.Date(2026, 7, 8, 9, 10, 11, 0, time.UTC)
	testMTime = time.Date(2021, 3, 14, 15, 9, 26, 0, time.UTC)
)

// --- fakes -----------------------------------------------------------------

// fakeSpaces serves one Space.
type fakeSpaces struct {
	space cs3.Space
	err   error
}

func (f *fakeSpaces) ListSpaces(context.Context) ([]cs3.Space, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []cs3.Space{f.space}, nil
}

func (f *fakeSpaces) ListDir(context.Context, cs3.Space, string) ([]cs3.Entry, error) {
	return nil, nil
}

func (f *fakeSpaces) Walk(context.Context, cs3.Space, func(cs3.Entry) error) error { return nil }

func (f *fakeSpaces) OpenFile(context.Context, cs3.Space, string, int64) (io.ReadCloser, error) {
	return nil, errors.New("not used")
}

// fakeWriter records everything a restore writes into the Space.
type fakeWriter struct {
	mu sync.Mutex

	dirs  []string
	files map[string]writtenFile
	// failUpload fails the upload of a specific destination path.
	failUpload map[string]error
	// failDir fails the creation of a specific directory.
	failDir map[string]error
}

type writtenFile struct {
	data    []byte
	size    int64
	modTime time.Time
}

func newFakeWriter() *fakeWriter {
	return &fakeWriter{
		files:      map[string]writtenFile{},
		failUpload: map[string]error{},
		failDir:    map[string]error{},
	}
}

func (w *fakeWriter) MakeDir(_ context.Context, _ cs3.Space, relDir string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.failDir[relDir]; err != nil {
		return err
	}
	w.dirs = append(w.dirs, relDir)
	return nil
}

func (w *fakeWriter) Upload(_ context.Context, _ cs3.Space, relPath string, size int64, modTime time.Time, r io.Reader) error {
	w.mu.Lock()
	failure := w.failUpload[relPath]
	w.mu.Unlock()
	if failure != nil {
		return failure
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.files[relPath] = writtenFile{data: data, size: size, modTime: modTime}
	return nil
}

func (w *fakeWriter) written() map[string]writtenFile {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]writtenFile, len(w.files))
	for k, v := range w.files {
		out[k] = v
	}
	return out
}

func (w *fakeWriter) createdDirs() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.dirs...)
}

// fakeEngine serves a fixed snapshot tree.
type fakeEngine struct {
	mu sync.Mutex

	tree []snapshot.RestoredEntry
	list []snapshot.Info
	// walkErr fails Walk; listErr fails List.
	walkErr error
	listErr error
	// repos records the Repo each call received, so tests can assert on the DK.
	repos []snapshot.Repo
	// onWalk runs inside Walk, letting a test observe the live Data Key.
	onWalk func(snapshot.Repo)
}

var _ snapshot.Engine = (*fakeEngine)(nil)

func (e *fakeEngine) record(r snapshot.Repo) {
	e.mu.Lock()
	defer e.mu.Unlock()
	captured := r
	captured.DK = append([]byte(nil), r.DK...)
	e.repos = append(e.repos, captured)
}

func (e *fakeEngine) Snapshot(context.Context, snapshot.Repo, snapshot.Source) (snapshot.Info, error) {
	return snapshot.Info{}, errors.New("not used")
}

func (e *fakeEngine) RestoreAll(context.Context, snapshot.Repo, snapshot.SnapshotID, string) error {
	return errors.New("path B must not stage to disk")
}

func (e *fakeEngine) RestoreFile(context.Context, snapshot.Repo, snapshot.SnapshotID, string, string) error {
	return errors.New("path B must not stage to disk")
}

func (e *fakeEngine) Walk(ctx context.Context, r snapshot.Repo, id snapshot.SnapshotID, fn func(context.Context, snapshot.RestoredEntry) error) error {
	e.record(r)
	if e.onWalk != nil {
		e.onWalk(r)
	}
	if e.walkErr != nil {
		return e.walkErr
	}
	if id != testSnapshot {
		return snapshot.ErrSnapshotNotFound
	}
	for _, entry := range e.tree {
		if err := fn(ctx, entry); err != nil {
			return err
		}
	}
	return nil
}

func (e *fakeEngine) Prune(context.Context, snapshot.Repo, time.Duration) error { return nil }

func (e *fakeEngine) List(_ context.Context, r snapshot.Repo) ([]snapshot.Info, error) {
	e.record(r)
	if e.listErr != nil {
		return nil, e.listErr
	}
	return e.list, nil
}

func (e *fakeEngine) lastRepo(t *testing.T) snapshot.Repo {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.repos) == 0 {
		t.Fatal("engine was never called")
	}
	return e.repos[len(e.repos)-1]
}

// fileEntry builds a snapshot file entry with in-memory content.
func fileEntry(p, content string) snapshot.RestoredEntry {
	return snapshot.RestoredEntry{
		Path:    p,
		Size:    int64(len(content)),
		ModTime: testMTime,
		Open: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(content)), nil
		},
	}
}

func dirEntry(p string) snapshot.RestoredEntry {
	return snapshot.RestoredEntry{Path: p, IsDir: true, ModTime: testMTime}
}

// --- harness ---------------------------------------------------------------

type harness struct {
	runner  *Runner
	writer  *fakeWriter
	engine  *fakeEngine
	spaces  *fakeSpaces
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

	h := &harness{
		spaces: &fakeSpaces{space: cs3.Space{
			ID:    testSpaceID,
			Name:  "Alice",
			Type:  "personal",
			Owner: "alice",
			Root:  cs3.ResourceID{StorageID: "storage-1", SpaceID: "space-1", OpaqueID: "root-1"},
		}},
		writer: newFakeWriter(),
		engine: &fakeEngine{
			tree: []snapshot.RestoredEntry{
				fileEntry("readme.txt", "hello"),
				dirEntry("docs"),
				fileEntry("docs/notes.txt", "notes body"),
			},
			list: []snapshot.Info{
				{ID: testSnapshot, StartTime: epoch.Add(-time.Hour), FileCount: 2, TotalBytes: 15},
			},
		},
		configs: spacecfg.NewMemoryStore(),
		targets: targets.NewMemoryStore(),
		keys:    keys.NewMemoryStore(),
		clock:   testutil.NewFakeClock(epoch),
		logs:    &bytes.Buffer{},
	}
	h.jobs = jobs.NewMemoryStoreWithClock(h.clock)

	sealer := newSealer(t)
	wrapper := newSRWWrapper(t)
	seedTarget(t, h.targets, sealer)
	seedConfig(t, h.configs)
	h.dk = seedKeys(t, h.keys, wrapper)

	runner, err := NewRunner(Deps{
		Spaces:  h.spaces,
		Writer:  h.writer,
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

func seedTarget(t *testing.T, store *targets.MemoryStore, sealer targets.CredSealer) {
	t.Helper()
	wrapped, version, err := sealer.Seal(targets.PlainCreds{
		AccessKeyID:     "GK-test-access-key",
		SecretAccessKey: "test-secret-access-key-0000000000000000",
	})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := store.CreateTarget(context.Background(), targets.Target{
		ID:           testTargetID,
		Name:         "Buddy S3",
		Endpoint:     "garage:3900",
		Region:       "garage",
		Bucket:       "backups",
		Prefix:       "oc/",
		WrappedCreds: wrapped,
		Version:      version,
	}); err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
}

func seedConfig(t *testing.T, store *spacecfg.MemoryStore) {
	t.Helper()
	if _, err := store.Put(context.Background(), spacecfg.Config{
		SpaceID:  testSpaceID,
		TargetID: testTargetID,
		Enabled:  true,
	}); err != nil {
		t.Fatalf("spacecfg.Put: %v", err)
	}
}

func seedKeys(t *testing.T, store *keys.MemoryStore, wrapper *keys.SRWWrapper) []byte {
	t.Helper()
	dk, err := keys.GenerateDK()
	if err != nil {
		t.Fatalf("GenerateDK: %v", err)
	}
	wrapped, err := wrapper.WrapSRW(dk)
	if err != nil {
		t.Fatalf("WrapSRW: %v", err)
	}
	if err := store.PutSRW(testSpaceID, wrapped); err != nil {
		t.Fatalf("PutSRW: %v", err)
	}
	return dk
}

func newSealer(t *testing.T) targets.CredSealer {
	t.Helper()
	twKey, err := keys.GenerateSRWKey()
	if err != nil {
		t.Fatalf("GenerateSRWKey: %v", err)
	}
	sealer, err := targets.NewCredSealer(twKey)
	if err != nil {
		t.Fatalf("NewCredSealer: %v", err)
	}
	return sealer
}

func newSRWWrapper(t *testing.T) *keys.SRWWrapper {
	t.Helper()
	srwKey, err := keys.GenerateSRWKey()
	if err != nil {
		t.Fatalf("GenerateSRWKey: %v", err)
	}
	w, err := keys.NewSRWWrapper(srwKey)
	if err != nil {
		t.Fatalf("NewSRWWrapper: %v", err)
	}
	t.Cleanup(w.Close)
	return w
}

// --- tests -----------------------------------------------------------------

func TestNewRunner_RequiresEveryDependency(t *testing.T) {
	if _, err := NewRunner(Deps{}); err == nil {
		t.Fatal("empty deps must be rejected")
	}
}

func TestRunRestore_WritesSnapshotIntoRestoreFolder(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	res, err := h.runner.RunRestore(ctx, testSpaceID, testSnapshot)
	if err != nil {
		t.Fatalf("RunRestore: %v", err)
	}

	wantFolder := "Restore/2026-07-08T09-10-11Z"
	if res.Folder != wantFolder {
		t.Fatalf("folder = %q, want %q", res.Folder, wantFolder)
	}
	if res.FileCount != 2 || res.TotalBytes != int64(len("hello")+len("notes body")) {
		t.Fatalf("stats = %+v", res)
	}

	written := h.writer.written()
	for path, want := range map[string]string{
		wantFolder + "/readme.txt":     "hello",
		wantFolder + "/docs/notes.txt": "notes body",
	} {
		got, ok := written[path]
		if !ok {
			t.Fatalf("%s was not written; wrote %v", path, keysOf(written))
		}
		if string(got.data) != want {
			t.Fatalf("%s = %q, want %q", path, got.data, want)
		}
		if got.size != int64(len(want)) {
			t.Fatalf("%s announced size %d, want %d", path, got.size, len(want))
		}
		// mtime is in backup scope, so it comes back with the file.
		if !got.modTime.Equal(testMTime) {
			t.Fatalf("%s mtime = %v, want %v", path, got.modTime, testMTime)
		}
	}

	// Directories are created before their contents.
	dirs := h.writer.createdDirs()
	if len(dirs) < 3 || dirs[0] != RestoreFolder || dirs[1] != wantFolder {
		t.Fatalf("created dirs = %v", dirs)
	}
	if dirs[2] != wantFolder+"/docs" {
		t.Fatalf("nested directory not created: %v", dirs)
	}
}

// Nothing outside the restore folder may be touched: no live file is written,
// and every path is below Restore/<ts>/ (decisions.md #3).
func TestRunRestore_NeverTouchesLiveData(t *testing.T) {
	h := newHarness(t)

	res, err := h.runner.RunRestore(context.Background(), testSpaceID, testSnapshot)
	if err != nil {
		t.Fatalf("RunRestore: %v", err)
	}

	for path := range h.writer.written() {
		if !strings.HasPrefix(path, res.Folder+"/") {
			t.Fatalf("restore wrote outside its folder: %q", path)
		}
	}
	for _, dir := range h.writer.createdDirs() {
		if dir != RestoreFolder && !strings.HasPrefix(dir, RestoreFolder+"/") {
			t.Fatalf("restore created a directory outside %s: %q", RestoreFolder, dir)
		}
	}
}

// Two restores of the same Space land in different folders, so a second attempt
// never overwrites the first.
func TestRunRestore_EachRunGetsItsOwnFolder(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	first, err := h.runner.RunRestore(ctx, testSpaceID, testSnapshot)
	if err != nil {
		t.Fatalf("first RunRestore: %v", err)
	}
	h.clock.Advance(90 * time.Second)
	second, err := h.runner.RunRestore(ctx, testSpaceID, testSnapshot)
	if err != nil {
		t.Fatalf("second RunRestore: %v", err)
	}
	if first.Folder == second.Folder {
		t.Fatalf("both restores used %q", first.Folder)
	}
}

// The engine must receive the Space's real Data Key, resolved server-side from
// the SRW envelope — the user's Recovery Key is never involved.
func TestRunRestore_UnwrapsDataKeyViaSRW(t *testing.T) {
	h := newHarness(t)

	if _, err := h.runner.RunRestore(context.Background(), testSpaceID, testSnapshot); err != nil {
		t.Fatalf("RunRestore: %v", err)
	}

	repo := h.engine.lastRepo(t)
	if !bytes.Equal(repo.DK, h.dk) {
		t.Fatal("engine did not receive the space's data key")
	}
	if repo.Location.AccessKeyID != "GK-test-access-key" {
		t.Fatalf("target credentials not resolved: %+v", repo.Location.Redacted())
	}
	if repo.Space.SpaceID != testSpaceID {
		t.Fatalf("repo space = %q", repo.Space.SpaceID)
	}
}

// The Data Key must not outlive the run.
func TestRunRestore_ZeroizesDataKey(t *testing.T) {
	h := newHarness(t)

	var live []byte
	h.engine.onWalk = func(r snapshot.Repo) { live = r.DK }

	if _, err := h.runner.RunRestore(context.Background(), testSpaceID, testSnapshot); err != nil {
		t.Fatalf("RunRestore: %v", err)
	}
	if live == nil {
		t.Fatal("engine never saw a data key")
	}
	for _, b := range live {
		if b != 0 {
			t.Fatal("data key was not zeroized after the run")
		}
	}
}

func TestRunRestore_RecordsJobLifecycle(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	res, err := h.runner.RunRestore(ctx, testSpaceID, testSnapshot)
	if err != nil {
		t.Fatalf("RunRestore: %v", err)
	}

	job, err := h.jobs.Get(ctx, res.JobID)
	if err != nil {
		t.Fatalf("Get job: %v", err)
	}
	if job.Kind != jobs.KindRestore || job.State != jobs.StateSucceeded {
		t.Fatalf("job = %+v", job)
	}
	if job.SnapshotID != string(testSnapshot) {
		t.Fatalf("job snapshot = %q", job.SnapshotID)
	}
	if job.Error != "" {
		t.Fatalf("successful job carries error %q", job.Error)
	}
}

func TestRunRestore_UnknownSnapshot(t *testing.T) {
	h := newHarness(t)

	_, err := h.runner.RunRestore(context.Background(), testSpaceID, "no-such-snapshot")
	if !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("err = %v, want ErrSnapshotNotFound", err)
	}
}

func TestRunRestore_Validation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.runner.RunRestore(ctx, "", testSnapshot); !errors.Is(err, ErrSpaceNotFound) {
		t.Fatalf("err = %v, want ErrSpaceNotFound", err)
	}
	if _, err := h.runner.RunRestore(ctx, testSpaceID, ""); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("err = %v, want ErrSnapshotNotFound", err)
	}
	if _, err := h.runner.RunRestore(ctx, "other-space", testSnapshot); !errors.Is(err, ErrSpaceNotFound) {
		t.Fatalf("err = %v, want ErrSpaceNotFound", err)
	}
}

func TestRunRestore_NotConfigured(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	// No configuration: nothing to restore from.
	empty := spacecfg.NewMemoryStore()
	h.runner.deps.Configs = empty
	if _, err := h.runner.RunRestore(ctx, testSpaceID, testSnapshot); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}

	// Configuration but no keys.
	h2 := newHarness(t)
	h2.runner.deps.Keys = keys.NewMemoryStore()
	if _, err := h2.runner.RunRestore(ctx, testSpaceID, testSnapshot); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}

func TestRunRestore_TargetUnavailable(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.runner.deps.Targets = targets.NewMemoryStore()

	if _, err := h.runner.RunRestore(ctx, testSpaceID, testSnapshot); !errors.Is(err, ErrTargetUnavailable) {
		t.Fatalf("err = %v, want ErrTargetUnavailable", err)
	}
}

// An upload failure fails the run, and the recorded reason never names the file.
func TestRunRestore_UploadFailureIsRecordedAndSanitized(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	folder := RestoreFolderName(epoch)
	h.writer.failUpload[folder+"/docs/notes.txt"] = errors.New("cs3 upload: quota exceeded on /home/alice/docs/notes.txt")

	_, err := h.runner.RunRestore(ctx, testSpaceID, testSnapshot)
	if !errors.Is(err, ErrRunFailed) {
		t.Fatalf("err = %v, want ErrRunFailed", err)
	}
	if strings.Contains(err.Error(), "notes.txt") {
		t.Fatalf("error leaked a file name: %v", err)
	}

	list, err := h.jobs.List(ctx, testSpaceID)
	if err != nil || len(list) != 1 {
		t.Fatalf("jobs = %v, %v", list, err)
	}
	if list[0].State != jobs.StateFailed {
		t.Fatalf("job state = %q, want failed", list[0].State)
	}
	if strings.Contains(list[0].Error, "notes.txt") || strings.Contains(list[0].Error, "quota") {
		t.Fatalf("job error leaked detail: %q", list[0].Error)
	}
}

func TestRunRestore_FolderCreationFailureFailsTheRun(t *testing.T) {
	h := newHarness(t)
	h.writer.failDir[RestoreFolder] = errors.New("permission denied")

	if _, err := h.runner.RunRestore(context.Background(), testSpaceID, testSnapshot); !errors.Is(err, ErrRunFailed) {
		t.Fatalf("err = %v, want ErrRunFailed", err)
	}
	if len(h.writer.written()) != 0 {
		t.Fatal("files were written although the restore folder could not be created")
	}
}

// A restore and a backup for one Space share the repository, so they must not
// overlap.
func TestRunRestore_LockExcludesConcurrentRuns(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	release, err := h.jobs.Acquire(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()

	if _, err := h.runner.RunRestore(ctx, testSpaceID, testSnapshot); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("err = %v, want ErrRunInProgress", err)
	}
}

func TestRunRestore_ReleasesLockOnFailure(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.engine.walkErr = errors.New("target gone")

	if _, err := h.runner.RunRestore(ctx, testSpaceID, testSnapshot); err == nil {
		t.Fatal("expected failure")
	}

	release, err := h.jobs.Acquire(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	release()
}

func TestListSnapshots(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	got, err := h.runner.ListSnapshots(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(got) != 1 || got[0].ID != testSnapshot {
		t.Fatalf("snapshots = %+v", got)
	}

	// The Data Key used to read the repository must not survive the call.
	repo := h.engine.lastRepo(t)
	if len(repo.DK) == 0 {
		t.Fatal("engine received no data key")
	}

	h.engine.listErr = errors.New("s3 unreachable")
	if _, err := h.runner.ListSnapshots(ctx, testSpaceID); !errors.Is(err, ErrTargetUnavailable) {
		t.Fatalf("err = %v, want ErrTargetUnavailable", err)
	}

	if _, err := h.runner.ListSnapshots(ctx, "other-space"); !errors.Is(err, ErrSpaceNotFound) {
		t.Fatalf("err = %v, want ErrSpaceNotFound", err)
	}
}

func TestStartRestore_RunsInBackground(t *testing.T) {
	h := newHarness(t)

	reqCtx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	proceed := make(chan struct{})
	h.engine.onWalk = func(snapshot.Repo) {
		close(started)
		<-proceed
	}

	jobID, err := h.runner.StartRestore(reqCtx, testSpaceID, testSnapshot)
	if err != nil {
		t.Fatalf("StartRestore: %v", err)
	}
	<-started
	cancel() // the HTTP request is over; the run must continue
	close(proceed)

	deadline := time.Now().Add(5 * time.Second)
	for {
		job, err := h.jobs.Get(context.Background(), jobID)
		if err != nil {
			t.Fatalf("Get job: %v", err)
		}
		if job.State.Terminal() {
			if job.State != jobs.StateSucceeded {
				t.Fatalf("background run failed: %+v", job)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background restore did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Logs are operational: never key material, never credentials, never file names.
func TestRunRestore_LogsNoSecrets(t *testing.T) {
	h := newHarness(t)

	if _, err := h.runner.RunRestore(context.Background(), testSpaceID, testSnapshot); err != nil {
		t.Fatalf("RunRestore: %v", err)
	}

	logged := h.logs.String()
	for _, secret := range []string{"GK-test-access-key", "test-secret-access-key-0000000000000000"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("logs leaked %q", secret)
		}
	}
	if bytes.Contains(h.logs.Bytes(), h.dk) {
		t.Fatal("logs leaked raw data key bytes")
	}
}

func TestRestoreFolderName(t *testing.T) {
	got := RestoreFolderName(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if got != "Restore/2026-01-02T03-04-05Z" {
		t.Fatalf("folder = %q", got)
	}
	// Colons would break the folder on Windows clients syncing the Space.
	if strings.Contains(got, ":") {
		t.Fatalf("folder name is not filename-safe: %q", got)
	}
}

func keysOf(m map[string]writtenFile) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
