// Package scheduler turns "back up my data" into "backed up while everyone
// sleeps": it decides which Spaces are due, starts their unattended runs, and
// keeps run history from growing forever.
//
// Design notes worth knowing before changing anything here:
//
//   - There is no in-memory schedule registry. Every tick reads the durable
//     per-Space configuration (pkg/spacecfg) and the durable run history
//     (pkg/jobs), so a restarted process picks up exactly where the old one
//     left off. State the scheduler keeps in memory is only about runs
//     currently in flight *in this process*.
//   - Due-ness is derived from the last *attempt*, not the last success. A
//     Space whose runs keep failing retries on its normal schedule instead of
//     hammering a broken target every tick; the stale-backup notification is
//     what makes sure the failure is not silent.
//   - Downtime therefore catches up exactly once: after three days offline the
//     next tick sees one overdue occurrence, runs it, and the run itself
//     becomes the new baseline. No storm of missed runs.
//   - Time is injected (Clock) and the tick body is exported (RunOnce), so the
//     scheduling rules are tested deterministically rather than by sleeping.
//
// Prune/maintenance is deliberately *not* scheduled here: it runs as a separate
// job with separate credentials (decisions.md #9 Tier 1, Phase 7).
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"time"

	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/spacecfg"
)

// Defaults chosen for a family-scale deployment: check often enough that a
// schedule feels honoured to the minute, run few enough Spaces at once that a
// home upstream link is not saturated.
const (
	// DefaultInterval is how often due-ness is evaluated.
	DefaultInterval = time.Minute
	// DefaultMaxConcurrent caps simultaneous scheduled runs.
	DefaultMaxConcurrent = 2
	// DefaultJitter staggers Spaces whose schedules coincide.
	DefaultJitter = 5 * time.Minute
	// DefaultPruneInterval is how often run history is trimmed.
	DefaultPruneInterval = time.Hour
	// DefaultSweepInterval is how often the staleness sweep runs. Staleness is
	// measured in days; checking it every minute only generates load.
	DefaultSweepInterval = 15 * time.Minute
	// historyLookback bounds how much history due-ness needs to inspect.
	historyLookback = 10
)

// Clock abstracts time for deterministic scheduler tests.
type Clock interface {
	Now() time.Time
}

// systemClock is the production Clock.
type systemClock struct{}

// SystemClock returns a Clock backed by the wall clock.
func SystemClock() Clock { return systemClock{} }

func (systemClock) Now() time.Time { return time.Now() }

// Runner executes one unattended backup run for a Space. It is satisfied by an
// adapter over *backup.Runner; the scheduler stays ignorant of the pipeline.
type Runner interface {
	RunScheduled(ctx context.Context, spaceID string) error
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(ctx context.Context, spaceID string) error

// RunScheduled calls f.
func (f RunnerFunc) RunScheduled(ctx context.Context, spaceID string) error { return f(ctx, spaceID) }

// Recoverer closes out runs abandoned by a process that died holding a lock.
// It is satisfied by *jobs.LeaseLocker.
type Recoverer interface {
	Recover(ctx context.Context, store jobs.Store) (int, error)
}

// RunGuard answers "is a run already under way for this Space", from the run
// lock rather than from run history. It is satisfied by *jobs.LeaseLocker.
type RunGuard interface {
	Busy(ctx context.Context, spaceID string) (bool, error)
}

// Pruner trims a durable collection to a retention window. Both the job store
// and the notification store satisfy it; the scheduler owns the cadence because
// it is the only component that ticks.
type Pruner interface {
	PruneBefore(ctx context.Context, cutoff time.Time) (int, error)
}

// Options tunes the scheduler. The zero value is usable.
type Options struct {
	// Interval between due-ness evaluations; zero uses DefaultInterval.
	Interval time.Duration
	// MaxConcurrent caps simultaneous runs; zero uses DefaultMaxConcurrent.
	MaxConcurrent int
	// Jitter is the window Spaces are staggered across; zero uses
	// DefaultJitter. Set it to zero-length explicitly via a negative value if
	// exact firing is ever wanted — it is not, for real deployments.
	Jitter time.Duration
	// HistoryWindow is how long finished runs are kept; zero uses
	// jobs.DefaultHistoryWindow.
	HistoryWindow time.Duration
	// PruneInterval is how often history is trimmed; zero uses
	// DefaultPruneInterval.
	PruneInterval time.Duration
	// SweepInterval is how often OnSweep is called; zero uses
	// DefaultSweepInterval.
	SweepInterval time.Duration
	// Location is the timezone schedules are interpreted in; nil uses UTC.
	// "Nightly at 02:30" means the family's night, not the server's idea of it.
	Location *time.Location
	// RunTimeout bounds one scheduled run; zero means no scheduler-imposed
	// bound (the runner has its own).
	RunTimeout time.Duration
}

// Deps are the scheduler's injected collaborators.
type Deps struct {
	// Configs is the durable source of truth for what is scheduled.
	Configs spacecfg.Store
	// Jobs supplies run history (due-ness) and history retention.
	Jobs jobs.Store
	// Runner performs the actual backup run.
	Runner Runner
	// Recoverer is optional; when set, abandoned runs are closed out each tick.
	Recoverer Recoverer
	// Runs is optional; when set, "is this Space already running" is answered
	// from the run lock instead of by reading run history every tick.
	Runs RunGuard
	// Events is optional; when set, the notification history is trimmed on the
	// same cadence and to the same window as the run history. Everything the
	// service keeps has to be trimmed by somebody.
	Events Pruner
	// Clock is injected for deterministic tests.
	Clock Clock
	// Logger receives operational detail; never key material.
	Logger *slog.Logger
	// OnRunFinished is called after every scheduled run, successful or not.
	// The notification layer hangs off this; the scheduler itself does not know
	// what a notification is.
	OnRunFinished func(ctx context.Context, spaceID string, err error)
	// OnSweep is called after due Spaces are dispatched, on its own slow
	// cadence (Options.SweepInterval). The stale-backup monitor hangs off this:
	// staleness is measured in days, so running it every tick would be pure
	// load for an answer that cannot have changed.
	OnSweep func(ctx context.Context, now time.Time)
}

// Scheduler dispatches due backup runs.
type Scheduler struct {
	deps Deps
	opts Options

	slots chan struct{}
	wg    sync.WaitGroup

	mu        sync.Mutex
	inflight  map[string]struct{}
	lastPrune time.Time
	lastSweep time.Time
}

// New validates dependencies and constructs a Scheduler.
func New(deps Deps, opts Options) (*Scheduler, error) {
	missing := map[string]bool{
		"config store": deps.Configs == nil,
		"job store":    deps.Jobs == nil,
		"runner":       deps.Runner == nil,
	}
	for name, isMissing := range missing {
		if isMissing {
			return nil, fmt.Errorf("scheduler: %s is required", name)
		}
	}
	if deps.Clock == nil {
		deps.Clock = systemClock{}
	}
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}

	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = DefaultMaxConcurrent
	}
	if opts.Jitter == 0 {
		opts.Jitter = DefaultJitter
	}
	if opts.Jitter < 0 {
		opts.Jitter = 0
	}
	if opts.HistoryWindow <= 0 {
		opts.HistoryWindow = jobs.DefaultHistoryWindow
	}
	if opts.PruneInterval <= 0 {
		opts.PruneInterval = DefaultPruneInterval
	}
	if opts.SweepInterval <= 0 {
		opts.SweepInterval = DefaultSweepInterval
	}
	if opts.Location == nil {
		opts.Location = time.UTC
	}

	return &Scheduler{
		deps:     deps,
		opts:     opts,
		slots:    make(chan struct{}, opts.MaxConcurrent),
		inflight: make(map[string]struct{}),
	}, nil
}

// Run evaluates schedules until ctx is cancelled, then waits for in-flight runs
// to unwind. A run interrupted by shutdown fails through the normal path and is
// recorded as failed, so history never shows a run that is "running" forever.
func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.opts.Interval)
	defer ticker.Stop()

	s.deps.Logger.Info("scheduler started",
		"interval", s.opts.Interval,
		"maxConcurrent", s.opts.MaxConcurrent,
		"jitter", s.opts.Jitter,
		"timezone", s.opts.Location.String(),
	)

	for {
		select {
		case <-ctx.Done():
			s.wg.Wait()
			s.deps.Logger.Info("scheduler stopped")
			return nil
		case <-ticker.C:
			if err := s.RunOnce(ctx); err != nil {
				// A tick failing is not fatal: the next one re-reads everything.
				s.deps.Logger.Error("scheduler tick failed", "err", err)
			}
		}
	}
}

// RunOnce performs a single evaluation: recover abandoned runs, dispatch due
// Spaces, trim history. It is exported so the scheduling rules can be tested
// without waiting for wall-clock ticks.
func (s *Scheduler) RunOnce(ctx context.Context) error {
	now := s.now()

	s.recoverAbandoned(ctx)

	configs, unreadable, err := s.deps.Configs.List(ctx)
	if err != nil {
		return fmt.Errorf("scheduler: list space configurations: %w", err)
	}
	// A configuration that cannot be read takes its Space out of the schedule.
	// Skipping it is right; skipping it quietly is not — that is a backup that
	// stops with nothing to show for it.
	for _, key := range unreadable {
		s.deps.Logger.Error("a space configuration could not be read; that space is not being scheduled",
			"document", key)
	}

	for _, cfg := range configs {
		if !s.eligible(cfg) {
			continue
		}
		due, err := s.due(ctx, cfg, now)
		if err != nil {
			s.deps.Logger.Error("could not evaluate schedule", "space", cfg.SpaceID, "err", err)
			continue
		}
		if !due {
			continue
		}
		if !s.dispatch(ctx, cfg.SpaceID) {
			// At capacity: the Space stays due and is picked up next tick.
			s.deps.Logger.Info("scheduled run deferred; concurrency limit reached", "space", cfg.SpaceID)
		}
	}

	s.pruneHistory(ctx, now)
	s.sweep(ctx, now)
	return nil
}

// Wait blocks until in-flight runs finish. Tests use it; Run does it on exit.
func (s *Scheduler) Wait() { s.wg.Wait() }

// eligible reports whether a Space takes part in scheduling at all.
func (s *Scheduler) eligible(cfg spacecfg.Config) bool {
	return cfg.Enabled && cfg.TargetID != ""
}

// due reports whether a Space's next occurrence has passed.
func (s *Scheduler) due(ctx context.Context, cfg spacecfg.Config, now time.Time) (bool, error) {
	busy, err := s.busy(ctx, cfg.SpaceID)
	if err != nil {
		return false, err
	}
	if busy {
		return false, nil
	}

	history, err := s.baselineHistory(ctx, cfg.SpaceID)
	if err != nil {
		return false, err
	}

	next, err := s.nextOccurrence(cfg, history, now)
	if err != nil {
		return false, err
	}
	if next.IsZero() {
		// A schedule that never fires again is not an error; it just never runs.
		return false, nil
	}
	return !now.Before(next), nil
}

// busy reports whether a run — of any kind, including a restore — is already
// under way for a Space.
//
// The run lock is the authority when there is one: it is a single read, it
// covers every process, and it expires, so a crashed run cannot make a Space
// look busy forever. Without a lock guard the question is answered from run
// history, which is what the scheduler did everywhere before and is kept for
// the in-memory locker the tests and the degraded deployment use.
func (s *Scheduler) busy(ctx context.Context, spaceID string) (bool, error) {
	if s.deps.Runs != nil {
		busy, err := s.deps.Runs.Busy(ctx, spaceID)
		if err != nil {
			return false, fmt.Errorf("scheduler: read run lock: %w", err)
		}
		return busy, nil
	}

	history, err := s.deps.Jobs.ListRecent(ctx, spaceID, historyLookback)
	if err != nil {
		return false, fmt.Errorf("scheduler: read run history: %w", err)
	}
	for _, j := range history {
		if !j.State.Terminal() {
			return true, nil
		}
	}
	return false, nil
}

// baselineHistory reads the least history that can answer "when did the last
// backup start": one record, widened only when the newest run turns out to be
// something else. Reading ten records per Space per minute to find one number
// is load an idle household deployment should not generate.
func (s *Scheduler) baselineHistory(ctx context.Context, spaceID string) ([]jobs.Job, error) {
	history, err := s.deps.Jobs.ListRecent(ctx, spaceID, 1)
	if err != nil {
		return nil, fmt.Errorf("scheduler: read run history: %w", err)
	}
	if _, ok := jobs.LastOf(history, jobs.KindBackup, ""); ok || len(history) == 0 {
		return history, nil
	}

	history, err = s.deps.Jobs.ListRecent(ctx, spaceID, historyLookback)
	if err != nil {
		return nil, fmt.Errorf("scheduler: read run history: %w", err)
	}
	return history, nil
}

// NextRun reports when a Space is next due, so the status board can say "next
// backup tonight at 02:30" using the scheduler's own arithmetic rather than a
// second, subtly different copy of it. The zero time means "not scheduled".
// An instant in the past means overdue: the next tick will pick it up.
func (s *Scheduler) NextRun(ctx context.Context, spaceID string) (time.Time, error) {
	cfg, err := s.deps.Configs.Get(ctx, spaceID)
	if err != nil {
		var notFound spacecfg.ErrNotFound
		if errors.As(err, &notFound) {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("scheduler: read space configuration: %w", err)
	}
	if !s.eligible(cfg) {
		return time.Time{}, nil
	}

	history, err := s.deps.Jobs.ListRecent(ctx, spaceID, historyLookback)
	if err != nil {
		return time.Time{}, fmt.Errorf("scheduler: read run history: %w", err)
	}
	return s.nextOccurrence(cfg, history, s.now())
}

// nextOccurrence is the single place the "when does this Space run next"
// question is answered: schedule, baseline and jitter, in that order.
func (s *Scheduler) nextOccurrence(cfg spacecfg.Config, history []jobs.Job, now time.Time) (time.Time, error) {
	base := s.baseline(cfg, history, now)
	next, err := NextAfter(cfg.EffectiveSchedule(), base.In(s.opts.Location))
	if err != nil {
		return time.Time{}, err
	}
	if next.IsZero() {
		return time.Time{}, nil
	}
	return next.Add(s.jitterFor(cfg.SpaceID)), nil
}

// baseline is the instant the next occurrence is computed from: the last
// attempted backup, or — for a Space that has never run — the moment it was
// configured.
func (s *Scheduler) baseline(cfg spacecfg.Config, history []jobs.Job, now time.Time) time.Time {
	if last, ok := jobs.LastOf(history, jobs.KindBackup, ""); ok {
		return last.CreatedAt
	}
	if !cfg.UpdatedAt.IsZero() {
		return cfg.UpdatedAt
	}
	return now
}

// jitterFor is a stable per-Space offset inside the jitter window. It is
// derived from the Space id rather than randomised so it survives restarts: a
// Space keeps its slot instead of wandering, and Spaces sharing a schedule stay
// spread out.
func (s *Scheduler) jitterFor(spaceID string) time.Duration {
	if s.opts.Jitter <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(spaceID))
	return time.Duration(h.Sum64() % uint64(s.opts.Jitter))
}

// dispatch starts a run if a concurrency slot is free. It reports whether the
// run was started.
func (s *Scheduler) dispatch(ctx context.Context, spaceID string) bool {
	s.mu.Lock()
	if _, running := s.inflight[spaceID]; running {
		s.mu.Unlock()
		return true // already running here; nothing deferred
	}
	select {
	case s.slots <- struct{}{}:
	default:
		s.mu.Unlock()
		return false
	}
	s.inflight[spaceID] = struct{}{}
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.inflight, spaceID)
			s.mu.Unlock()
			<-s.slots
		}()
		s.execute(ctx, spaceID)
	}()
	return true
}

// execute performs one run and reports its outcome.
func (s *Scheduler) execute(ctx context.Context, spaceID string) {
	runCtx := ctx
	if s.opts.RunTimeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, s.opts.RunTimeout)
		defer cancel()
	}

	s.deps.Logger.Info("starting scheduled backup", "space", spaceID)
	err := s.deps.Runner.RunScheduled(runCtx, spaceID)
	if err != nil {
		// The runner has already recorded a sanitized failure on the job.
		s.deps.Logger.Error("scheduled backup failed", "space", spaceID, "err", err)
	}

	if s.deps.OnRunFinished != nil {
		// The hook must run even when the scheduler is shutting down: a failure
		// nobody is told about is the exact problem notifications exist for.
		hookCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		s.deps.OnRunFinished(hookCtx, spaceID, err)
	}
}

// recoverAbandoned closes out runs whose process disappeared.
func (s *Scheduler) recoverAbandoned(ctx context.Context) {
	if s.deps.Recoverer == nil {
		return
	}
	n, err := s.deps.Recoverer.Recover(ctx, s.deps.Jobs)
	if err != nil {
		s.deps.Logger.Error("could not recover abandoned runs", "err", err)
		return
	}
	if n > 0 {
		s.deps.Logger.Warn("closed out abandoned runs", "jobs", n)
	}
}

// pruneHistory trims finished runs — and the notifications about them — older
// than the history window, on its own slow cadence so it does not run on every
// tick.
func (s *Scheduler) pruneHistory(ctx context.Context, now time.Time) {
	if !s.lastPrune.IsZero() && now.Sub(s.lastPrune) < s.opts.PruneInterval {
		return
	}
	s.lastPrune = now
	cutoff := now.Add(-s.opts.HistoryWindow)

	removed, err := s.deps.Jobs.PruneBefore(ctx, cutoff)
	if err != nil {
		s.deps.Logger.Error("could not prune run history", "err", err)
	} else if removed > 0 {
		s.deps.Logger.Info("pruned run history", "jobs", removed)
	}

	if s.deps.Events == nil {
		return
	}
	// Notifications are written on the same cadence as the runs they describe,
	// so they grow at the same rate and are kept for the same window.
	removed, err = s.deps.Events.PruneBefore(ctx, cutoff)
	if err != nil {
		s.deps.Logger.Error("could not prune notification history", "err", err)
		return
	}
	if removed > 0 {
		s.deps.Logger.Info("pruned notification history", "events", removed)
	}
}

// sweep runs the staleness check on its own cadence.
func (s *Scheduler) sweep(ctx context.Context, now time.Time) {
	if s.deps.OnSweep == nil {
		return
	}
	if !s.lastSweep.IsZero() && now.Sub(s.lastSweep) < s.opts.SweepInterval {
		return
	}
	s.lastSweep = now
	s.deps.OnSweep(ctx, now)
}

// now returns the current time in the scheduler's timezone.
func (s *Scheduler) now() time.Time {
	return s.deps.Clock.Now().In(s.opts.Location)
}
