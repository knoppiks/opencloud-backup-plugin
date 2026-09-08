//go:build integration

package backup

// Phase-6 exit criteria, end to end against a real S3 target (ephemeral Garage):
//
//   - an unattended run completes with **no user session** — nothing but stored
//     configuration, the clock and the SRW-wrapped Data Key,
//   - restart safety: a process that dies mid-run loses neither the history nor
//     the Space, and produces no duplicate run,
//   - the stale-backup notification fires when backups quietly stop.
//
// The status/history HTTP contract is exercised in pkg/api (the API package
// imports this one, so it cannot be imported back from here).
//
// The "disk" is a state.Store shared across two sets of stores: constructing a
// fresh set from the same backing store is exactly what a restart does, minus
// the process boundary.
//
// Run: go test -tags integration -run TestIntegration_Scheduled ./pkg/backup/...

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/notify"
	"opencloud-backup-plugin/pkg/scheduler"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/state"
	"opencloud-backup-plugin/pkg/takeout"
	"opencloud-backup-plugin/pkg/targets"
)

// scheduleEpoch is midnight, so "30 2 * * *" is 2.5 hours away.
var scheduleEpoch = time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC)

const nightly = "30 2 * * *"

// scheduledFixture is one "process": stores over shared durable state, plus the
// worker and scheduler built on them.
type scheduledFixture struct {
	t       *testing.T
	backing state.Store
	clock   *testutil.FakeClock

	configs *spacecfg.StateStore
	jobs    *jobs.StateStore
	keys    *keys.StateStore
	targets *targets.StateStore
	events  *notify.StateStore
	locker  *jobs.LeaseLocker

	runner  *Runner
	sched   *scheduler.Scheduler
	monitor *notify.Monitor
	reader  *fakeReader
	engine  *snapshot.KopiaEngine
	repo    snapshot.Repo
}

// clusterSecrets are the things a restart does NOT change: the wrapping keys
// live in cluster secrets, and the Space's Data Key is whatever its stored SRW
// envelope holds. Sharing them across processes in a test is what makes the
// restart faithful.
type clusterSecrets struct {
	sealer  targets.CredSealer
	wrapper *keys.SRWWrapper
	dk      []byte
}

func newClusterSecrets(t *testing.T) clusterSecrets {
	t.Helper()
	dk, err := keys.GenerateDK()
	if err != nil {
		t.Fatalf("GenerateDK: %v", err)
	}
	return clusterSecrets{sealer: newSealer(t), wrapper: newSRWWrapper(t), dk: dk}
}

// startProcess builds a fresh set of stores over the given durable state — a
// service start. leaseTTL is short in tests so crash recovery is observable.
func startProcess(
	ctx context.Context,
	t *testing.T,
	backing state.Store,
	clock *testutil.FakeClock,
	garage *testutil.Garage,
	reader *fakeReader,
	secrets clusterSecrets,
	seed bool,
	leaseTTL time.Duration,
) *scheduledFixture {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	f := &scheduledFixture{
		t:       t,
		backing: backing,
		clock:   clock,
		configs: spacecfg.NewStateStore(backing, clock),
		jobs:    jobs.NewStateStore(backing, clock),
		keys:    keys.NewStateStore(backing, clock),
		targets: targets.NewStateStore(backing),
		events:  notify.NewStateStore(backing, clock),
		reader:  reader,
	}

	locker, err := jobs.NewLeaseLocker(backing, jobs.LeaseOptions{
		TTL:    leaseTTL,
		Clock:  clock,
		Logger: logger,
		// Renewals are never triggered in these tests: a run either completes
		// inside its lease or is meant to look abandoned.
		After: func(time.Duration) <-chan time.Time { return make(chan time.Time) },
	})
	if err != nil {
		t.Fatalf("NewLeaseLocker: %v", err)
	}
	f.locker = locker

	engine, err := snapshot.NewEngine(snapshot.S3Opener{}, snapshot.EngineOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	f.engine = engine

	sealer, wrapper := secrets.sealer, secrets.wrapper

	if seed {
		seedStateTarget(t, f.targets, sealer, garage)
		if _, err := f.configs.Put(ctx, spacecfg.Config{
			SpaceID:  testSpaceID,
			TargetID: testTargetID,
			Schedule: nightly,
			Enabled:  true,
		}); err != nil {
			t.Fatalf("spacecfg.Put: %v", err)
		}
	}

	// The Data Key is stored once, SRW-wrapped. A restarted process reads that
	// same envelope back — which is the whole point: an unattended run needs no
	// user session, only the server wrap (decisions.md #1).
	if seed {
		wrapped, err := wrapper.WrapSRW(secrets.dk)
		if err != nil {
			t.Fatalf("WrapSRW: %v", err)
		}
		if err := f.keys.PutSRW(testSpaceID, wrapped); err != nil {
			t.Fatalf("PutSRW: %v", err)
		}
	} else if _, err := f.keys.GetSRW(testSpaceID); err != nil {
		t.Fatalf("the stored key envelope did not survive the restart: %v", err)
	}

	runner, err := NewRunner(Deps{
		Spaces:    reader,
		Configs:   f.configs,
		Targets:   f.targets,
		Sealer:    sealer,
		Keys:      f.keys,
		Unwrap:    wrapper,
		Engine:    engine,
		Jobs:      f.jobs,
		Locks:     locker,
		Envelopes: takeout.S3Publisher{},
		Logger:    logger,
		Clock:     clock,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	f.runner = runner

	notifier, err := notify.New(f.events, notify.Options{
		Sinks:  []notify.Sink{notify.LogSink{Logger: logger}},
		Clock:  clock,
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("notify.New: %v", err)
	}
	monitor, err := notify.NewMonitor(notify.MonitorDeps{
		Configs:  f.configs,
		Jobs:     f.jobs,
		Events:   f.events,
		Notifier: notifier,
		Logger:   logger,
	}, notify.MonitorOptions{})
	if err != nil {
		t.Fatalf("notify.NewMonitor: %v", err)
	}
	f.monitor = monitor
	reporter := notify.NewReporter(notifier, nil, logger)

	sched, err := scheduler.New(scheduler.Deps{
		Configs: f.configs,
		Jobs:    f.jobs,
		Runner: scheduler.RunnerFunc(func(ctx context.Context, spaceID string) error {
			_, err := runner.RunScheduled(ctx, spaceID)
			return err
		}),
		Recoverer:     locker,
		Runs:          locker,
		Events:        f.events,
		Clock:         clock,
		Logger:        logger,
		OnRunFinished: reporter.RunFinished,
		OnSweep:       monitor.Sweep,
		// The exit-criteria tests drive ticks by hand and expect each one to
		// sweep; production spaces them out (DefaultSweepInterval).
	}, scheduler.Options{Jitter: -1, SweepInterval: time.Nanosecond})
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	f.sched = sched

	f.repo = snapshot.Repo{
		Location: garageLocation(garage),
		Space:    snapshot.SpaceRef{SpaceID: testSpaceID},
		DK:       secrets.dk,
	}
	return f
}

func (f *scheduledFixture) tick(ctx context.Context) {
	f.t.Helper()
	if err := f.sched.RunOnce(ctx); err != nil {
		f.t.Fatalf("RunOnce: %v", err)
	}
	f.sched.Wait()
}

// seedStateTarget stores the Garage instance as a TW-sealed target.
func seedStateTarget(t *testing.T, store targets.Store, sealer targets.CredSealer, g *testutil.Garage) {
	t.Helper()
	wrapped, version, err := sealer.Seal(targets.PlainCreds{
		AccessKeyID:     g.AccessKeyID,
		SecretAccessKey: g.SecretAccessKey,
	})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	loc := garageLocation(g)
	if _, err := store.CreateTarget(context.Background(), targets.Target{
		ID:           testTargetID,
		Name:         "Garage",
		Endpoint:     loc.Endpoint,
		Region:       loc.Region,
		Bucket:       loc.Bucket,
		Prefix:       testPrefix,
		UsePathStyle: true,
		DisableTLS:   true,
		WrappedCreds: wrapped,
		Version:      version,
	}); err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
}

func newScheduledSpace() *fakeReader {
	space := cs3.Space{
		ID:    testSpaceID,
		Name:  "Alice",
		Type:  "personal",
		Owner: "alice",
		Root:  cs3.ResourceID{StorageID: "storage-1", SpaceID: "space-1", OpaqueID: "root-1"},
	}
	reader := newFakeReader(space)
	reader.put("readme.txt", []byte("scheduled backup content"), testMTime)
	reader.put("docs/notes.txt", []byte("more scheduled content"), testMTime)
	return reader
}

// The headline exit criterion: a backup happens with nobody logged in.
func TestIntegration_ScheduledRunNeedsNoUserSession(t *testing.T) {
	ctx := context.Background()
	garage := testutil.StartGarage(ctx, t)
	clock := testutil.NewFakeClock(scheduleEpoch)
	backing := state.NewMemoryStore()

	f := startProcess(ctx, t, backing, clock, garage, newScheduledSpace(), newClusterSecrets(t), true, 10*time.Minute)

	// Before the scheduled time nothing happens...
	f.tick(ctx)
	history, err := f.jobs.List(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("a run happened before its time: %+v", history)
	}

	// ...and after it, a full snapshot lands on the target.
	clock.Set(scheduleEpoch.Add(2*time.Hour + 31*time.Minute))
	f.tick(ctx)

	history, err = f.jobs.List(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("runs = %d, want exactly one scheduled run", len(history))
	}
	run := history[0]
	if run.State != jobs.StateSucceeded {
		t.Fatalf("run = %+v", run)
	}
	if run.Trigger != jobs.TriggerSchedule {
		t.Fatalf("trigger = %q, want the run to be recorded as scheduled", run.Trigger)
	}
	if run.SnapshotID == "" || run.FileCount == 0 || run.TotalBytes == 0 {
		t.Fatalf("run did not record its outcome: %+v", run)
	}

	snapshots, err := f.engine.List(ctx, f.repo)
	if err != nil {
		t.Fatalf("List snapshots: %v", err)
	}
	if len(snapshots) != 1 || string(snapshots[0].ID) != run.SnapshotID {
		t.Fatalf("snapshots = %+v, want the one the scheduled run produced", snapshots)
	}

	// The run lock is released, so the Space is usable again.
	release, err := f.locker.Acquire(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("Acquire after the run: %v", err)
	}
	release()
}

// Restart safety: state outlives the process, and a restart neither loses a run
// nor repeats one.
func TestIntegration_ScheduledRunSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	garage := testutil.StartGarage(ctx, t)
	clock := testutil.NewFakeClock(scheduleEpoch)
	backing := state.NewMemoryStore()
	reader := newScheduledSpace()

	secrets := newClusterSecrets(t)
	first := startProcess(ctx, t, backing, clock, garage, reader, secrets, true, 10*time.Minute)
	clock.Set(scheduleEpoch.Add(2*time.Hour + 31*time.Minute))
	first.tick(ctx)

	before, err := first.jobs.List(ctx, testSpaceID)
	if err != nil || len(before) != 1 {
		t.Fatalf("history = %+v (%v)", before, err)
	}

	// A new process over the same durable state.
	second := startProcess(ctx, t, backing, clock, garage, reader, secrets, false, 10*time.Minute)

	after, err := second.jobs.List(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("List after restart: %v", err)
	}
	if len(after) != 1 || after[0].ID != before[0].ID || after[0].SnapshotID != before[0].SnapshotID {
		t.Fatalf("history after restart = %+v, want the first process's run", after)
	}

	// The schedule survived too, and the restart does not re-run what already
	// ran in this window.
	cfg, err := second.configs.Get(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("Get config after restart: %v", err)
	}
	if cfg.Schedule != nightly || !cfg.Enabled {
		t.Fatalf("configuration after restart = %+v", cfg)
	}

	clock.Advance(time.Minute)
	second.tick(ctx)
	final, err := second.jobs.List(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(final) != 1 {
		t.Fatalf("runs after restart = %d, want no duplicate", len(final))
	}
}

// A process that dies mid-run leaves a job "running" and a lease held. The next
// process must close the job out and take the Space back — once the lease has
// expired, and not before.
func TestIntegration_CrashedRunIsRecovered(t *testing.T) {
	ctx := context.Background()
	garage := testutil.StartGarage(ctx, t)
	clock := testutil.NewFakeClock(scheduleEpoch)
	backing := state.NewMemoryStore()
	reader := newScheduledSpace()

	secrets := newClusterSecrets(t)
	crashed := startProcess(ctx, t, backing, clock, garage, reader, secrets, true, 10*time.Minute)

	// Simulate a run that started and whose process then disappeared.
	if _, err := crashed.locker.Acquire(ctx, testSpaceID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	abandoned, err := crashed.jobs.Create(ctx, jobs.Job{
		SpaceID: testSpaceID,
		Kind:    jobs.KindBackup,
		State:   jobs.StateRunning,
		Trigger: jobs.TriggerSchedule,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	restarted := startProcess(ctx, t, backing, clock, garage, reader, secrets, false, 10*time.Minute)

	// While the lease is live, the abandoned run is left alone and the Space is
	// not started again: a slow run must not be killed by a restart.
	clock.Advance(5 * time.Minute)
	restarted.tick(ctx)
	still, err := restarted.jobs.Get(ctx, abandoned.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if still.State != jobs.StateRunning {
		t.Fatalf("a live lease was reaped: %+v", still)
	}
	history, err := restarted.jobs.List(ctx, testSpaceID)
	if err != nil || len(history) != 1 {
		t.Fatalf("history = %+v (%v), want no second run while one is in flight", history, err)
	}

	// Once the lease has expired and the schedule comes due, the abandoned run
	// is closed out and the Space runs again.
	clock.Set(scheduleEpoch.Add(2*time.Hour + 31*time.Minute))
	restarted.tick(ctx)

	recovered, err := restarted.jobs.Get(ctx, abandoned.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if recovered.State != jobs.StateFailed || recovered.Error != jobs.MessageInterrupted {
		t.Fatalf("abandoned run = %+v, want it closed out as interrupted", recovered)
	}

	history, err = restarted.jobs.List(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history = %+v, want the recovered run plus a fresh one", history)
	}
	if newest := history[0]; newest.State != jobs.StateSucceeded {
		t.Fatalf("the run after recovery = %+v", newest)
	}
}

// Backups that quietly stop must be reported. This is the silent-failure case
// the whole notification layer exists for: runs keep being attempted, they keep
// failing, and without a notification nobody would ever look.
func TestIntegration_StaleBackupIsReported(t *testing.T) {
	ctx := context.Background()
	garage := testutil.StartGarage(ctx, t)
	clock := testutil.NewFakeClock(scheduleEpoch)
	backing := state.NewMemoryStore()

	f := startProcess(ctx, t, backing, clock, garage, newScheduledSpace(), newClusterSecrets(t), true, 10*time.Minute)

	clock.Set(scheduleEpoch.Add(2*time.Hour + 31*time.Minute))
	f.tick(ctx)

	// The target disappears — an admin deleted it, or its credentials were
	// revoked. Runs keep being attempted and keep failing.
	if err := f.targets.DeleteTarget(ctx, testTargetID); err != nil {
		t.Fatalf("DeleteTarget: %v", err)
	}
	for day := 1; day <= 3; day++ {
		clock.Set(scheduleEpoch.Add(time.Duration(day)*24*time.Hour + 2*time.Hour + 31*time.Minute))
		f.tick(ctx)
	}

	history, err := f.jobs.List(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(history) != 4 {
		t.Fatalf("runs = %d, want the successful one plus three failures", len(history))
	}
	if history[0].State != jobs.StateFailed {
		t.Fatalf("latest run = %+v, want a failure", history[0])
	}

	events, err := f.events.List(ctx, testSpaceID, 0)
	if err != nil {
		t.Fatalf("List events: %v", err)
	}
	var stale, failed []notify.Event
	for _, e := range events {
		switch e.Kind {
		case notify.KindBackupStale:
			stale = append(stale, e)
		case notify.KindRunFailed:
			failed = append(failed, e)
		}
	}
	if len(failed) != 3 {
		t.Fatalf("run-failure notifications = %d, want one per failed run", len(failed))
	}
	if len(stale) != 1 {
		t.Fatalf("stale notifications = %+v, want exactly one", stale)
	}
	if stale[0].Audience != notify.AudienceSpaceMembers || stale[0].SpaceID != testSpaceID {
		t.Fatalf("stale notification = %+v", stale[0])
	}

	// Nothing about this Space may reach the operator's feed (decisions.md #15).
	operator, err := f.events.ListOperator(ctx, 0)
	if err != nil {
		t.Fatalf("ListOperator: %v", err)
	}
	for _, e := range operator {
		if e.SpaceID != "" {
			t.Fatalf("operator event names a space: %+v", e)
		}
	}
}
