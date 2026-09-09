package scheduler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/spacecfg"
)

var epoch = time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC)

// fakeRunner records the runs it was asked for and, when blocking, holds them
// open so concurrency limits can be observed.
//
// It writes a job record like the real runner does, because due-ness is derived
// from run history: a fake that skipped that would make the scheduler look like
// it loops when it does not.
type fakeRunner struct {
	store jobs.Store

	mu      sync.Mutex
	runs    []string
	err     error
	started chan string
	release chan struct{}
}

func (f *fakeRunner) RunScheduled(ctx context.Context, spaceID string) error {
	f.mu.Lock()
	f.runs = append(f.runs, spaceID)
	started, release, runErr := f.started, f.release, f.err
	f.mu.Unlock()

	job, err := f.store.Create(ctx, jobs.Job{
		SpaceID: spaceID,
		Kind:    jobs.KindBackup,
		State:   jobs.StateRunning,
		Trigger: jobs.TriggerSchedule,
	})
	if err != nil {
		return err
	}

	if started != nil {
		started <- spaceID
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			runErr = ctx.Err()
		}
	}

	outcome := jobs.Outcome{State: jobs.StateSucceeded}
	if runErr != nil {
		outcome = jobs.Outcome{State: jobs.StateFailed, Error: "the backup run failed"}
	}
	if err := f.store.Finish(ctx, job.ID, outcome); err != nil {
		return err
	}
	return runErr
}

func (f *fakeRunner) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.runs...)
}

type harness struct {
	t       *testing.T
	clock   *testutil.FakeClock
	configs *spacecfg.MemoryStore
	jobs    *jobs.MemoryStore
	runner  *fakeRunner
	sched   *Scheduler
	logs    bytes.Buffer
}

func newHarness(t *testing.T, opts Options, tweaks ...func(*Deps)) *harness {
	t.Helper()

	clock := testutil.NewFakeClock(epoch)
	jobStore := jobs.NewMemoryStoreWithClock(clock)
	h := &harness{
		t:       t,
		clock:   clock,
		configs: spacecfg.NewMemoryStoreWithClock(clock),
		jobs:    jobStore,
		runner:  &fakeRunner{store: jobStore},
	}

	deps := Deps{
		Configs: h.configs,
		Jobs:    h.jobs,
		Runner:  h.runner,
		Clock:   clock,
		Logger:  slog.New(slog.NewTextHandler(&h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	for _, tweak := range tweaks {
		tweak(&deps)
	}

	sched, err := New(deps, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.sched = sched
	return h
}

// configure registers an enabled Space with the given schedule.
func (h *harness) configure(spaceID, schedule string) {
	h.t.Helper()
	if _, err := h.configs.Put(context.Background(), spacecfg.Config{
		SpaceID:  spaceID,
		TargetID: "t1",
		Schedule: schedule,
		Enabled:  true,
	}); err != nil {
		h.t.Fatalf("configure %s: %v", spaceID, err)
	}
}

func (h *harness) tick() {
	h.t.Helper()
	if err := h.sched.RunOnce(context.Background()); err != nil {
		h.t.Fatalf("RunOnce: %v", err)
	}
	h.sched.Wait()
}

func TestNew_RequiresCollaborators(t *testing.T) {
	t.Parallel()

	full := Deps{
		Configs: spacecfg.NewMemoryStore(),
		Jobs:    jobs.NewMemoryStore(),
		Runner:  &fakeRunner{store: jobs.NewMemoryStore()},
	}
	if _, err := New(full, Options{}); err != nil {
		t.Fatalf("New: %v", err)
	}

	withoutConfigs := full
	withoutConfigs.Configs = nil
	if _, err := New(withoutConfigs, Options{}); err == nil {
		t.Fatal("missing config store must be rejected")
	}
	withoutJobs := full
	withoutJobs.Jobs = nil
	if _, err := New(withoutJobs, Options{}); err == nil {
		t.Fatal("missing job store must be rejected")
	}
	withoutRunner := full
	withoutRunner.Runner = nil
	if _, err := New(withoutRunner, Options{}); err == nil {
		t.Fatal("missing runner must be rejected")
	}
}

func TestRunOnce_RunsAtTheScheduledTime(t *testing.T) {
	h := newHarness(t, Options{Jitter: -1})
	h.configure("s1", "30 2 * * *")

	// Before the occurrence: nothing.
	h.clock.Set(epoch.Add(2 * time.Hour))
	h.tick()
	if got := h.runner.calls(); len(got) != 0 {
		t.Fatalf("ran before its time: %v", got)
	}

	// After it: exactly one run.
	h.clock.Set(epoch.Add(2*time.Hour + 31*time.Minute))
	h.tick()
	if got := h.runner.calls(); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("runs = %v, want one run of s1", got)
	}

	// The run itself becomes the baseline, so the next tick does nothing.
	h.clock.Advance(time.Minute)
	h.tick()
	if got := h.runner.calls(); len(got) != 1 {
		t.Fatalf("runs = %v, want no second run in the same window", got)
	}

	// Next day, it runs again.
	h.clock.Set(epoch.Add(26*time.Hour + 31*time.Minute))
	h.tick()
	if got := h.runner.calls(); len(got) != 2 {
		t.Fatalf("runs = %v, want a run on the following day", got)
	}
}

func TestRunOnce_SkipsDisabledAndUnboundSpaces(t *testing.T) {
	h := newHarness(t, Options{Jitter: -1})
	ctx := context.Background()

	if _, err := h.configs.Put(ctx, spacecfg.Config{SpaceID: "disabled", TargetID: "t1", Enabled: false}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	h.configure("enabled", "30 2 * * *")

	h.clock.Set(epoch.Add(3 * time.Hour))
	h.tick()

	got := h.runner.calls()
	if len(got) != 1 || got[0] != "enabled" {
		t.Fatalf("runs = %v, want only the enabled space", got)
	}
}

// Downtime must produce exactly one catch-up run, not one per missed day.
func TestRunOnce_CatchesUpOnce(t *testing.T) {
	h := newHarness(t, Options{Jitter: -1})
	ctx := context.Background()
	h.configure("s1", "30 2 * * *")

	// A run happened, then the service was down for three days.
	j, err := h.jobs.Create(ctx, jobs.Job{SpaceID: "s1", Kind: jobs.KindBackup, State: jobs.StateRunning, Trigger: jobs.TriggerSchedule})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := h.jobs.Finish(ctx, j.ID, jobs.Outcome{State: jobs.StateSucceeded}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	h.clock.Set(epoch.Add(72 * time.Hour))
	h.tick()
	if got := h.runner.calls(); len(got) != 1 {
		t.Fatalf("runs = %v, want a single catch-up run", got)
	}

	// And the catch-up must not immediately trigger another.
	h.clock.Advance(time.Minute)
	h.tick()
	if got := h.runner.calls(); len(got) != 1 {
		t.Fatalf("runs = %v, want no storm after catching up", got)
	}
}

// A Space with a run already under way must not get a second one, whatever the
// schedule says. The per-Space lock is the real guard; this keeps the scheduler
// from generating pointless failures against it.
func TestRunOnce_SkipsSpacesWithAnUnfinishedRun(t *testing.T) {
	h := newHarness(t, Options{Jitter: -1})
	ctx := context.Background()
	h.configure("s1", "30 2 * * *")

	if _, err := h.jobs.Create(ctx, jobs.Job{SpaceID: "s1", Kind: jobs.KindRestore, State: jobs.StateRunning}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	h.clock.Set(epoch.Add(3 * time.Hour))
	h.tick()
	if got := h.runner.calls(); len(got) != 0 {
		t.Fatalf("runs = %v, want none while another run is in flight", got)
	}
}

// Jitter must stagger Spaces that share a schedule, stay inside its window, and
// be stable across restarts (it is derived from the Space id, not randomised).
func TestJitterIsBoundedStableAndSpreads(t *testing.T) {
	h := newHarness(t, Options{Jitter: 10 * time.Minute})

	offsets := map[string]time.Duration{}
	for _, id := range []string{"s1", "s2", "s3", "s4", "s5"} {
		off := h.sched.jitterFor(id, h.sched.opts.Jitter)
		if off < 0 || off >= 10*time.Minute {
			t.Fatalf("jitter for %s = %v, outside the window", id, off)
		}
		if again := h.sched.jitterFor(id, h.sched.opts.Jitter); again != off {
			t.Fatalf("jitter for %s is not stable: %v then %v", id, off, again)
		}
		offsets[id] = off
	}

	distinct := map[time.Duration]struct{}{}
	for _, off := range offsets {
		distinct[off] = struct{}{}
	}
	if len(distinct) < 2 {
		t.Fatalf("jitter did not spread anything out: %v", offsets)
	}

	// A fresh scheduler (a restart) must place the same Space in the same slot.
	other := newHarness(t, Options{Jitter: 10 * time.Minute})
	for id, off := range offsets {
		if got := other.sched.jitterFor(id, other.sched.opts.Jitter); got != off {
			t.Fatalf("jitter for %s changed across restart: %v -> %v", id, off, got)
		}
	}
}

func TestJitterDelaysTheRun(t *testing.T) {
	h := newHarness(t, Options{Jitter: time.Hour})
	h.configure("s1", "30 2 * * *")

	offset := h.sched.jitterFor("s1", h.sched.opts.Jitter)
	if offset < time.Minute {
		t.Skip("this space's jitter offset is too small to observe")
	}

	h.clock.Set(epoch.Add(2*time.Hour + 30*time.Minute))
	h.tick()
	if got := h.runner.calls(); len(got) != 0 {
		t.Fatalf("ran before its jittered slot: %v", got)
	}

	h.clock.Set(epoch.Add(2*time.Hour + 30*time.Minute).Add(offset))
	h.tick()
	if got := h.runner.calls(); len(got) != 1 {
		t.Fatalf("runs = %v, want one run once the jitter elapsed", got)
	}
}

func TestRunOnce_HonoursTheConcurrencyCap(t *testing.T) {
	h := newHarness(t, Options{Jitter: -1, MaxConcurrent: 2})
	h.runner.started = make(chan string, 8)
	h.runner.release = make(chan struct{})

	for _, id := range []string{"s1", "s2", "s3", "s4"} {
		h.configure(id, "30 2 * * *")
	}
	h.clock.Set(epoch.Add(3 * time.Hour))

	if err := h.sched.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Exactly two runs may be in flight; the rest are deferred.
	for range 2 {
		select {
		case <-h.runner.started:
		case <-time.After(2 * time.Second):
			t.Fatal("expected two runs to start")
		}
	}
	select {
	case id := <-h.runner.started:
		t.Fatalf("a third run (%s) started despite the cap", id)
	case <-time.After(100 * time.Millisecond):
	}

	close(h.runner.release)
	h.sched.Wait()

	if got := h.runner.calls(); len(got) != 2 {
		t.Fatalf("runs = %v, want exactly the cap", got)
	}

	// The deferred Spaces are still due and run on the next tick.
	h.runner.release = nil
	h.runner.started = nil
	h.tick()
	if got := h.runner.calls(); len(got) != 4 {
		t.Fatalf("runs = %v, want the deferred spaces to run next tick", got)
	}
}

func TestRunOnce_ReportsOutcomesToTheHook(t *testing.T) {
	clock := testutil.NewFakeClock(epoch)
	configs := spacecfg.NewMemoryStoreWithClock(clock)
	jobStore := jobs.NewMemoryStoreWithClock(clock)
	runner := &fakeRunner{store: jobStore, err: errors.New("target unreachable")}

	var (
		mu      sync.Mutex
		reports []error
	)
	sched, err := New(Deps{
		Configs: configs,
		Jobs:    jobStore,
		Runner:  runner,
		Clock:   clock,
		OnRunFinished: func(_ context.Context, _ string, err error) {
			mu.Lock()
			defer mu.Unlock()
			reports = append(reports, err)
		},
	}, Options{Jitter: -1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := configs.Put(context.Background(), spacecfg.Config{
		SpaceID:  "s1",
		TargetID: "t1",
		Schedule: "30 2 * * *",
		Enabled:  true,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	clock.Set(epoch.Add(3 * time.Hour))
	if err := sched.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	sched.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 1 || reports[0] == nil {
		t.Fatalf("hook reports = %v, want one failure", reports)
	}
}

// A failing Space must retry on its normal schedule, not on every tick: the
// stale-backup notification is what surfaces the problem, not a hot loop.
func TestRunOnce_FailedRunsDoNotRetryImmediately(t *testing.T) {
	clock := testutil.NewFakeClock(epoch)
	configs := spacecfg.NewMemoryStoreWithClock(clock)
	jobStore := jobs.NewMemoryStoreWithClock(clock)

	// A runner that records a failed job, the way the real one does.
	runner := RunnerFunc(func(ctx context.Context, spaceID string) error {
		j, err := jobStore.Create(ctx, jobs.Job{
			SpaceID: spaceID,
			Kind:    jobs.KindBackup,
			State:   jobs.StateRunning,
			Trigger: jobs.TriggerSchedule,
		})
		if err != nil {
			return err
		}
		return jobStore.Finish(ctx, j.ID, jobs.Outcome{State: jobs.StateFailed, Error: "the backup target is unavailable"})
	})

	sched, err := New(Deps{Configs: configs, Jobs: jobStore, Runner: runner, Clock: clock}, Options{Jitter: -1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := configs.Put(context.Background(), spacecfg.Config{
		SpaceID:  "s1",
		TargetID: "t1",
		Schedule: "30 2 * * *",
		Enabled:  true,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	clock.Set(epoch.Add(3 * time.Hour))
	for range 5 {
		if err := sched.RunOnce(context.Background()); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
		sched.Wait()
		clock.Advance(time.Minute)
	}

	history, err := jobStore.List(context.Background(), "s1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("%d runs recorded, want a single attempt until the next occurrence", len(history))
	}
}

func TestRunOnce_RecoversAbandonedRuns(t *testing.T) {
	h := newHarness(t, Options{Jitter: -1})
	recoverer := &fakeRecoverer{}
	sched, err := New(Deps{
		Configs:   h.configs,
		Jobs:      h.jobs,
		Runner:    h.runner,
		Clock:     h.clock,
		Recoverer: recoverer,
	}, Options{Jitter: -1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := sched.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if recoverer.calls != 1 {
		t.Fatalf("recoverer called %d times, want 1", recoverer.calls)
	}
}

type fakeRecoverer struct{ calls int }

func (f *fakeRecoverer) Recover(context.Context, jobs.Store) (int, error) {
	f.calls++
	return 0, nil
}

func TestPruneHistory_RunsOnItsOwnCadence(t *testing.T) {
	h := newHarness(t, Options{
		Jitter:          -1,
		HistoryWindow:   24 * time.Hour,
		HistoryInterval: time.Hour,
	})
	ctx := context.Background()

	stale, err := h.jobs.Create(ctx, jobs.Job{SpaceID: "s1", Kind: jobs.KindBackup, State: jobs.StateRunning})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := h.jobs.Finish(ctx, stale.ID, jobs.Outcome{State: jobs.StateSucceeded}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// Not yet older than the window: kept.
	h.clock.Set(epoch.Add(2 * time.Hour))
	h.tick()
	if _, err := h.jobs.Get(ctx, stale.ID); err != nil {
		t.Fatalf("job pruned too early: %v", err)
	}

	// Older than the window, and past the prune cadence: dropped.
	h.clock.Set(epoch.Add(48 * time.Hour))
	h.tick()
	if _, err := h.jobs.Get(ctx, stale.ID); err == nil {
		t.Fatal("job older than the history window was not pruned")
	}
}

// The status board asks the scheduler itself when a Space runs next, so the two
// cannot drift apart.
func TestNextRun(t *testing.T) {
	h := newHarness(t, Options{Jitter: -1})
	ctx := context.Background()

	// Unknown or unconfigured Space: no next run, and no error.
	next, err := h.sched.NextRun(ctx, "unknown")
	if err != nil || !next.IsZero() {
		t.Fatalf("NextRun(unknown) = %v, %v", next, err)
	}

	h.configure("s1", "30 2 * * *")
	next, err = h.sched.NextRun(ctx, "s1")
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	want := epoch.Add(2*time.Hour + 30*time.Minute)
	if !next.Equal(want) {
		t.Fatalf("NextRun = %v, want %v", next, want)
	}

	// A disabled Space is not scheduled at all.
	if _, err := h.configs.Put(ctx, spacecfg.Config{SpaceID: "s1", TargetID: "t1", Schedule: "30 2 * * *"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	next, err = h.sched.NextRun(ctx, "s1")
	if err != nil || !next.IsZero() {
		t.Fatalf("NextRun(disabled) = %v, %v", next, err)
	}
}

// The reported next run must include the Space's jitter, or the status board
// would promise a time the scheduler does not honour.
func TestNextRunIncludesJitter(t *testing.T) {
	h := newHarness(t, Options{Jitter: time.Hour})
	h.configure("s1", "30 2 * * *")

	next, err := h.sched.NextRun(context.Background(), "s1")
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	want := epoch.Add(2*time.Hour + 30*time.Minute).Add(h.sched.jitterFor("s1", h.sched.opts.Jitter))
	if !next.Equal(want) {
		t.Fatalf("NextRun = %v, want %v", next, want)
	}
}

func TestRun_StopsOnContextCancel(t *testing.T) {
	h := newHarness(t, Options{Interval: time.Millisecond, Jitter: -1})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.sched.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}

// The whole point of the phase: a run happens with nobody logged in, driven
// only by stored configuration and the clock.
func TestRun_UnattendedRunHappensWithNoRequest(t *testing.T) {
	clock := testutil.NewFakeClock(epoch)
	configs := spacecfg.NewMemoryStoreWithClock(clock)
	jobStore := jobs.NewMemoryStoreWithClock(clock)
	runner := &fakeRunner{store: jobStore, started: make(chan string, 1)}

	sched, err := New(Deps{Configs: configs, Jobs: jobStore, Runner: runner, Clock: clock},
		Options{Interval: time.Millisecond, Jitter: -1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := configs.Put(context.Background(), spacecfg.Config{
		SpaceID:  "s1",
		TargetID: "t1",
		Schedule: "30 2 * * *",
		Enabled:  true,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Past the occurrence, with no request in sight.
	clock.Set(epoch.Add(3 * time.Hour))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = sched.Run(ctx) }()

	select {
	case id := <-runner.started:
		if id != "s1" {
			t.Fatalf("ran %q", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no unattended run happened")
	}
}
