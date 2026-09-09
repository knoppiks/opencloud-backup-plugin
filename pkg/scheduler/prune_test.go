package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/jobs"
)

// annualSchedule fires once a year, so a test can observe prune dispatch on
// its own without a backup falling due and taking the Space's turn.
const annualSchedule = "0 3 1 1 *"

// fakePruneRunner records the prunes it was asked for and, like the real
// runner, writes a job record — prune cadence is derived from run history, so a
// fake that skipped that would make the scheduler look like it loops.
type fakePruneRunner struct {
	store jobs.Store

	mu    sync.Mutex
	runs  []string
	err   error
	state jobs.State
}

func (f *fakePruneRunner) RunPrune(ctx context.Context, spaceID string) error {
	f.mu.Lock()
	f.runs = append(f.runs, spaceID)
	runErr, state := f.err, f.state
	f.mu.Unlock()

	job, err := f.store.Create(ctx, jobs.Job{
		SpaceID: spaceID,
		Kind:    jobs.KindPrune,
		State:   jobs.StateRunning,
		Trigger: jobs.TriggerSchedule,
	})
	if err != nil {
		return err
	}
	if state == "" {
		state = jobs.StateSucceeded
		if runErr != nil {
			state = jobs.StateFailed
		}
	}
	if err := f.store.Finish(ctx, job.ID, jobs.Outcome{State: state}); err != nil {
		return err
	}
	return runErr
}

func (f *fakePruneRunner) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.runs...)
}

// prunableHarness is a scheduler with prune wired and a schedule that will not
// fire again today, so prune dispatch can be observed on its own.
func prunableHarness(t *testing.T, opts Options, tweaks ...func(*Deps)) (*harness, *fakePruneRunner) {
	t.Helper()

	pruner := &fakePruneRunner{}
	all := append([]func(*Deps){func(d *Deps) {
		pruner.store = d.Jobs
		d.Prunes = pruner
	}}, tweaks...)
	return newHarness(t, opts, all...), pruner
}

// seedBackup records a finished backup for a Space at the harness's current
// time, which is both what a prune needs to exist and what stops the backup
// schedule from firing again immediately.
func seedBackup(t *testing.T, h *harness, spaceID string, state jobs.State) jobs.Job {
	t.Helper()
	ctx := context.Background()

	job, err := h.jobs.Create(ctx, jobs.Job{SpaceID: spaceID, Kind: jobs.KindBackup, State: jobs.StateRunning})
	if err != nil {
		t.Fatalf("seed backup: %v", err)
	}
	if err := h.jobs.Finish(ctx, job.ID, jobs.Outcome{State: state}); err != nil {
		t.Fatalf("finish seeded backup: %v", err)
	}
	return job
}

// Until a Space has actually been backed up there is no repository to open, so
// a prune could only fail — every day, for as long as the Space stays broken.
func TestPrune_WaitsForASuccessfulBackup(t *testing.T) {
	h, pruner := prunableHarness(t, Options{Jitter: -1})
	h.configure("s1", annualSchedule)

	// Nothing has ever run.
	h.clock.Set(epoch.Add(3 * time.Hour))
	h.tick()
	if got := pruner.calls(); len(got) != 0 {
		t.Fatalf("pruned a space that was never backed up: %v", got)
	}

	// A failed backup is not a repository either. A run that never got past a
	// checkpoint lands here too: an incomplete run is not a success.
	seedBackup(t, h, "s1", jobs.StateFailed)
	h.tick()
	if got := pruner.calls(); len(got) != 0 {
		t.Fatalf("pruned a space whose backups fail: %v", got)
	}

	h.clock.Advance(time.Minute)
	seedBackup(t, h, "s1", jobs.StateSucceeded)
	h.tick()
	if got := pruner.calls(); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("prunes = %v, want one for s1", got)
	}
}

// Retention is applied on a slow cadence of its own: full maintenance is the
// most expensive thing this service does to a target, and expiry is measured in
// days.
func TestPrune_RunsOnItsOwnCadence(t *testing.T) {
	h, pruner := prunableHarness(t, Options{Jitter: -1, PruneInterval: 24 * time.Hour})
	h.configure("s1", annualSchedule)
	seedBackup(t, h, "s1", jobs.StateSucceeded)

	h.clock.Set(epoch.Add(3 * time.Hour))
	h.tick()
	if got := pruner.calls(); len(got) != 1 {
		t.Fatalf("prunes = %v, want the first one", got)
	}

	// Ticking again straight away must not prune again.
	h.tick()
	h.clock.Advance(time.Hour)
	h.tick()
	if got := pruner.calls(); len(got) != 1 {
		t.Fatalf("prunes = %v, want the cadence to hold", got)
	}

	// A day later — plus this Space's stable offset inside the window — it is
	// due again.
	offset := h.sched.jitterFor("s1", h.sched.opts.PruneInterval/pruneJitterFraction)
	h.clock.Advance(24*time.Hour + offset)
	h.tick()
	if got := pruner.calls(); len(got) != 2 {
		t.Fatalf("prunes = %v, want a second one a day later", got)
	}
}

// Spaces sharing a deployment must not all prune at the same instant forever
// after their first, simultaneous one.
func TestPrune_StaggersSpacesAcrossTheInterval(t *testing.T) {
	h, _ := prunableHarness(t, Options{Jitter: -1})

	offsets := make(map[string]time.Duration)
	for _, id := range []string{"s1", "s2", "s3", "s4", "s5"} {
		offsets[id] = h.sched.jitterFor(id, h.sched.opts.PruneInterval/pruneJitterFraction)
	}

	distinct := make(map[time.Duration]struct{}, len(offsets))
	for _, off := range offsets {
		if off >= h.sched.opts.PruneInterval/pruneJitterFraction {
			t.Fatalf("offset %s escaped the stagger window", off)
		}
		distinct[off] = struct{}{}
	}
	if len(distinct) < 2 {
		t.Fatalf("prune stagger spread nothing out: %v", offsets)
	}
}

// A prune that keeps failing retries on its cadence rather than on every tick:
// due-ness is measured from the last attempt, exactly as a backup's is.
func TestPrune_AFailingPruneDoesNotRetryEveryTick(t *testing.T) {
	h, pruner := prunableHarness(t, Options{Jitter: -1})
	pruner.err = errors.New("target unreachable")
	h.configure("s1", annualSchedule)
	seedBackup(t, h, "s1", jobs.StateSucceeded)

	h.clock.Set(epoch.Add(3 * time.Hour))
	for range 5 {
		h.clock.Advance(time.Minute)
		h.tick()
	}
	if got := pruner.calls(); len(got) != 1 {
		t.Fatalf("prunes = %v, want the failure to wait for the next cadence", got)
	}
	if !strings.Contains(h.logs.String(), "scheduled prune failed") {
		t.Fatalf("the failure was not reported:\n%s", h.logs.String())
	}
}

// Both share the Space's run lock. Dispatching a prune alongside a due backup
// would only record a job that immediately fails with "a run is in progress".
func TestPrune_YieldsToADueBackup(t *testing.T) {
	h, pruner := prunableHarness(t, Options{Jitter: -1})
	h.configure("s1", "* * * * *")
	seedBackup(t, h, "s1", jobs.StateSucceeded)

	h.clock.Advance(2 * time.Minute)
	h.tick()

	if got := h.runner.calls(); len(got) != 1 {
		t.Fatalf("backups = %v, want the due backup to run", got)
	}
	if got := pruner.calls(); len(got) != 0 {
		t.Fatalf("prunes = %v, want none while a backup was due", got)
	}

	// With the backup no longer due, the prune gets its turn.
	h.tick()
	if got := pruner.calls(); len(got) != 1 {
		t.Fatalf("prunes = %v, want one once the backup was not due", got)
	}
}

// A prune failure means storage was not reclaimed. That is an operator's
// problem; telling a family their backup failed would be false.
func TestPrune_DoesNotNotifyMembers(t *testing.T) {
	var notified []string
	h, pruner := prunableHarness(t, Options{Jitter: -1}, func(d *Deps) {
		d.OnRunFinished = func(_ context.Context, spaceID string, _ error) {
			notified = append(notified, spaceID)
		}
	})
	pruner.err = errors.New("target unreachable")
	h.configure("s1", annualSchedule)
	seedBackup(t, h, "s1", jobs.StateSucceeded)

	h.clock.Set(epoch.Add(3 * time.Hour))
	h.tick()

	if got := pruner.calls(); len(got) != 1 {
		t.Fatalf("prunes = %v, want one", got)
	}
	if len(notified) != 0 {
		t.Fatalf("a prune notified members: %v", notified)
	}
}

// Nothing is dispatched for a Space another process is already running.
func TestPrune_SkippedWhileTheSpaceIsBusy(t *testing.T) {
	h, pruner := prunableHarness(t, Options{Jitter: -1}, func(d *Deps) {
		d.Runs = &busyGuard{busy: true}
	})
	h.configure("s1", annualSchedule)
	seedBackup(t, h, "s1", jobs.StateSucceeded)

	h.clock.Set(epoch.Add(3 * time.Hour))
	h.tick()
	if got := pruner.calls(); len(got) != 0 {
		t.Fatalf("prunes = %v, want none while the space is busy", got)
	}
}

// Without a prune runner the scheduler behaves exactly as it did before there
// was one: nothing expires, and nothing pretends to.
func TestPrune_NotScheduledWithoutARunner(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, Options{Jitter: -1})
	h.configure("s1", annualSchedule)
	seedBackup(t, h, "s1", jobs.StateSucceeded)

	h.clock.Set(epoch.Add(3 * time.Hour))
	h.tick()

	history, err := h.jobs.ListRecentOfKind(ctx, "s1", jobs.KindPrune, 0)
	if err != nil {
		t.Fatalf("ListRecentOfKind: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("prune jobs = %+v, want none", history)
	}
}
