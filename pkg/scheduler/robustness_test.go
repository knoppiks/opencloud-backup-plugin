package scheduler

// What the tick does besides dispatching: report what it could not read, trim
// what it keeps, and ask the cheap question instead of the expensive one.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/spacecfg"
)

// brokenConfigs reports one unreadable document alongside whatever the real
// store holds, as the durable store does when a document will not decode.
type brokenConfigs struct {
	spacecfg.Store

	unreadable []string
}

func (b brokenConfigs) List(ctx context.Context) ([]spacecfg.Config, []string, error) {
	configs, _, err := b.Store.List(ctx)
	return configs, b.unreadable, err
}

// A Space whose configuration cannot be read drops out of the schedule. That is
// the right thing to do and the wrong thing to do silently: it is a backup that
// stops with nothing anywhere to say so.
func TestRunOnce_ReportsAConfigurationItCannotRead(t *testing.T) {
	h := newHarness(t, Options{Jitter: -1}, func(d *Deps) {
		d.Configs = brokenConfigs{Store: d.Configs, unreadable: []string{"spaceconfigs/s2/000"}}
	})
	h.configure("s1", "* * * * *")

	h.clock.Advance(2 * time.Minute)
	h.tick()

	logs := h.logs.String()
	if !strings.Contains(logs, "spaceconfigs/s2/000") {
		t.Fatalf("the unreadable document was not named in the log:\n%s", logs)
	}
	if !strings.Contains(logs, "not being scheduled") {
		t.Fatalf("the consequence was not stated in the log:\n%s", logs)
	}
	// The readable Spaces carry on: one bad record must not stop the schedule.
	if got := h.runner.calls(); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("runs = %v, want the readable space to have run", got)
	}
}

// fakePruner records the cutoffs it was asked to prune to.
type fakePruner struct {
	mu      sync.Mutex
	cutoffs []time.Time
	err     error
}

func (p *fakePruner) PruneBefore(_ context.Context, cutoff time.Time) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cutoffs = append(p.cutoffs, cutoff)
	return 1, p.err
}

func (p *fakePruner) calls() []time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]time.Time(nil), p.cutoffs...)
}

// Everything the service keeps has to be trimmed by somebody. Notifications
// grow at the same rate as the runs they describe, so they are trimmed on the
// same cadence and to the same window.
func TestPruneHistory_TrimsNotificationsToo(t *testing.T) {
	events := &fakePruner{}
	window := 30 * 24 * time.Hour
	h := newHarness(t, Options{Jitter: -1, HistoryWindow: window, HistoryInterval: time.Hour}, func(d *Deps) {
		d.Events = events
	})

	h.tick()
	calls := events.calls()
	if len(calls) != 1 {
		t.Fatalf("notification prunes = %d, want 1", len(calls))
	}
	if want := h.clock.Now().Add(-window); !calls[0].Equal(want) {
		t.Fatalf("cutoff = %v, want %v", calls[0], want)
	}

	// Not on every tick: the prune has its own slow cadence.
	h.clock.Advance(time.Minute)
	h.tick()
	if got := len(events.calls()); got != 1 {
		t.Fatalf("notification prunes = %d, want the cadence honoured", got)
	}

	h.clock.Advance(time.Hour)
	h.tick()
	if got := len(events.calls()); got != 2 {
		t.Fatalf("notification prunes = %d, want a second one after the interval", got)
	}
}

// A prune failure is reported and does not stop the tick.
func TestPruneHistory_SurvivesAFailingNotificationPrune(t *testing.T) {
	h := newHarness(t, Options{Jitter: -1}, func(d *Deps) {
		d.Events = &fakePruner{err: errors.New("store unavailable")}
	})
	h.configure("s1", "* * * * *")

	h.clock.Advance(2 * time.Minute)
	h.tick()

	if !strings.Contains(h.logs.String(), "could not prune notification history") {
		t.Fatalf("the failure was not reported:\n%s", h.logs.String())
	}
	if got := h.runner.calls(); len(got) != 1 {
		t.Fatalf("runs = %v, want the tick to have carried on", got)
	}
}

// Staleness is measured in days. Checking it every minute is load spent on an
// answer that cannot have changed.
func TestSweep_RunsOnItsOwnCadence(t *testing.T) {
	var sweeps int
	h := newHarness(t, Options{Jitter: -1, SweepInterval: 15 * time.Minute}, func(d *Deps) {
		d.OnSweep = func(context.Context, time.Time) { sweeps++ }
	})

	h.tick()
	if sweeps != 1 {
		t.Fatalf("sweeps = %d, want the first tick to sweep", sweeps)
	}
	for i := 0; i < 14; i++ {
		h.clock.Advance(time.Minute)
		h.tick()
	}
	if sweeps != 1 {
		t.Fatalf("sweeps = %d, want one until the interval passes", sweeps)
	}

	h.clock.Advance(time.Minute)
	h.tick()
	if sweeps != 2 {
		t.Fatalf("sweeps = %d, want a second sweep after the interval", sweeps)
	}
}

// busyGuard answers the "is a run under way" question from a fixed answer.
type busyGuard struct {
	busy  bool
	err   error
	calls int
}

func (g *busyGuard) Busy(context.Context, string) (bool, error) {
	g.calls++
	return g.busy, g.err
}

// With a run lock available, "already running" is one read of the lock rather
// than a scan of run history — and the lock, unlike history, expires.
func TestDue_AsksTheRunLock(t *testing.T) {
	guard := &busyGuard{busy: true}
	h := newHarness(t, Options{Jitter: -1}, func(d *Deps) { d.Runs = guard })
	h.configure("s1", "* * * * *")

	h.clock.Advance(2 * time.Minute)
	h.tick()

	if guard.calls == 0 {
		t.Fatal("the run lock was never consulted")
	}
	if got := h.runner.calls(); len(got) != 0 {
		t.Fatalf("runs = %v, want none while the lock is held", got)
	}

	guard.busy = false
	h.tick()
	if got := h.runner.calls(); len(got) != 1 {
		t.Fatalf("runs = %v, want one once the lock is free", got)
	}
}

// The wedge, seen from the scheduler: a job stuck at "running" that no lock
// corresponds to used to make a Space permanently ineligible. With the lock as
// the authority it does not.
func TestDue_IsNotBlockedByAStaleRunningRecord(t *testing.T) {
	guard := &busyGuard{}
	h := newHarness(t, Options{Jitter: -1}, func(d *Deps) { d.Runs = guard })
	h.configure("s1", "* * * * *")

	if _, err := h.jobs.Create(context.Background(), jobs.Job{
		SpaceID: "s1",
		Kind:    jobs.KindBackup,
		State:   jobs.StateRunning,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h.clock.Advance(2 * time.Minute)
	h.tick()

	if got := h.runner.calls(); len(got) != 1 {
		t.Fatalf("runs = %v, want the space to be eligible again", got)
	}
}

// A lock that cannot be read is not permission to start a second run.
func TestDue_FailsClosedWhenTheRunLockCannotBeRead(t *testing.T) {
	h := newHarness(t, Options{Jitter: -1}, func(d *Deps) {
		d.Runs = &busyGuard{err: errors.New("state store unavailable")}
	})
	h.configure("s1", "* * * * *")

	h.clock.Advance(2 * time.Minute)
	h.tick()

	if got := h.runner.calls(); len(got) != 0 {
		t.Fatalf("runs = %v, want none while the lock is unreadable", got)
	}
	if !strings.Contains(h.logs.String(), "could not evaluate schedule") {
		t.Fatalf("the failure was not reported:\n%s", h.logs.String())
	}
}

// The baseline is the last *backup*. Restores and prunes share the same
// history and are newer more often than not; treating one of those as the
// baseline would schedule a spurious extra backup, and failing to find any
// backup would fall back to the configuration timestamp and do the same.
func TestBaseline_IgnoresRunsThatAreNotBackups(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, Options{Jitter: -1})

	backup, err := h.jobs.Create(ctx, jobs.Job{SpaceID: "s1", Kind: jobs.KindBackup, State: jobs.StateSucceeded})
	if err != nil {
		t.Fatalf("seed backup: %v", err)
	}
	for _, kind := range []jobs.Kind{jobs.KindRestore, jobs.KindPrune} {
		h.clock.Advance(time.Hour)
		if _, err := h.jobs.Create(ctx, jobs.Job{SpaceID: "s1", Kind: kind, State: jobs.StateSucceeded}); err != nil {
			t.Fatalf("seed %s: %v", kind, err)
		}
	}

	cfg := spacecfg.Config{SpaceID: "s1", UpdatedAt: epoch.Add(-time.Hour)}
	last, ok, err := h.sched.lastRunOf(ctx, "s1", jobs.KindBackup)
	if err != nil {
		t.Fatalf("lastRunOf: %v", err)
	}
	base := h.sched.baseline(cfg, last, ok, h.clock.Now())
	if !base.Equal(backup.CreatedAt) {
		t.Fatalf("baseline = %s, want the last backup at %s", base, backup.CreatedAt)
	}
}

// A Space that has never been backed up is scheduled from the moment it was
// configured, not from now — otherwise its first run is always one interval
// away, however long ago it was set up.
func TestBaseline_FallsBackToTheConfiguration(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, Options{Jitter: -1})

	configured := epoch.Add(-3 * time.Hour)
	last, ok, err := h.sched.lastRunOf(ctx, "s1", jobs.KindBackup)
	if err != nil {
		t.Fatalf("lastRunOf: %v", err)
	}
	base := h.sched.baseline(spacecfg.Config{SpaceID: "s1", UpdatedAt: configured}, last, ok, h.clock.Now())
	if !base.Equal(configured) {
		t.Fatalf("baseline = %s, want the configuration time %s", base, configured)
	}
}
