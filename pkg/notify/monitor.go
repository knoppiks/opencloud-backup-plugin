package notify

// Stale-backup detection.
//
// A failed run announces itself. A Space that simply stopped running — because
// its schedule was never due, its target vanished, or the service was down for
// a fortnight — announces nothing, and that is the failure mode this product
// most needs to catch: the family believes they are covered and they are not.
//
// The threshold is derived from the Space's own schedule rather than fixed, so
// a nightly Space is stale after two nights while a weekly one is not stale
// until it has missed two weeks. A fixed threshold would either cry wolf at the
// weekly Spaces or stay silent far too long for the nightly ones.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/scheduler"
	"opencloud-backup-plugin/pkg/spacecfg"
)

const (
	// DefaultMinStaleAfter is the floor on the staleness threshold, whatever
	// the schedule says. Nothing is reported stale sooner than this.
	DefaultMinStaleAfter = 36 * time.Hour
	// DefaultRepeatAfter is how long before an unchanged stale Space is
	// reported again. Nagging daily is how notifications get muted.
	DefaultRepeatAfter = 7 * 24 * time.Hour
	// staleFactor is how many scheduled occurrences may be missed before a
	// Space counts as stale.
	staleFactor = 2
	// historyLookback bounds the history a staleness check reads.
	historyLookback = 20
)

// MonitorOptions tunes staleness detection. The zero value is usable.
type MonitorOptions struct {
	// MinStaleAfter is the floor on the threshold; zero uses the default.
	MinStaleAfter time.Duration
	// RepeatAfter is the re-notification interval; zero uses the default.
	RepeatAfter time.Duration
}

// Monitor reports Spaces whose backups have gone stale.
type Monitor struct {
	configs  spacecfg.Store
	jobs     jobs.Store
	events   Store
	notifier *Notifier
	logger   *slog.Logger
	opts     MonitorOptions
}

// MonitorDeps are the Monitor's collaborators.
type MonitorDeps struct {
	Configs  spacecfg.Store
	Jobs     jobs.Store
	Events   Store
	Notifier *Notifier
	Logger   *slog.Logger
}

// NewMonitor constructs a Monitor.
func NewMonitor(deps MonitorDeps, opts MonitorOptions) (*Monitor, error) {
	missing := map[string]bool{
		"config store": deps.Configs == nil,
		"job store":    deps.Jobs == nil,
		"event store":  deps.Events == nil,
		"notifier":     deps.Notifier == nil,
	}
	for name, isMissing := range missing {
		if isMissing {
			return nil, fmt.Errorf("notify: %s is required", name)
		}
	}
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.MinStaleAfter <= 0 {
		opts.MinStaleAfter = DefaultMinStaleAfter
	}
	if opts.RepeatAfter <= 0 {
		opts.RepeatAfter = DefaultRepeatAfter
	}
	return &Monitor{
		configs:  deps.Configs,
		jobs:     deps.Jobs,
		events:   deps.Events,
		notifier: deps.Notifier,
		logger:   deps.Logger,
		opts:     opts,
	}, nil
}

// Sweep checks every scheduled Space and notifies about the stale ones. It is
// safe to call on every scheduler tick: re-notification is rate-limited.
func (m *Monitor) Sweep(ctx context.Context, now time.Time) {
	configs, unreadable, err := m.configs.List(ctx)
	if err != nil {
		m.logger.Error("could not read space configurations for staleness check", "err", err)
		return
	}
	m.reportUnreadable(ctx, len(unreadable), now)

	for _, cfg := range configs {
		if !cfg.Enabled {
			// A Space with backups switched off is not stale, it is off.
			continue
		}
		stale, since, err := m.isStale(ctx, cfg, now)
		if err != nil {
			m.logger.Error("could not evaluate backup staleness", "space", cfg.SpaceID, "err", err)
			continue
		}
		if !stale {
			continue
		}
		notified, err := m.notifiedRecently(ctx, cfg.SpaceID, now)
		if err != nil {
			m.logger.Error("could not read notification history", "space", cfg.SpaceID, "err", err)
			continue
		}
		if notified {
			continue
		}
		if _, err := m.notifier.SpaceEvent(ctx, KindBackupStale, cfg.SpaceID, staleMessage(since)); err != nil {
			m.logger.Error("could not record stale-backup notification", "space", cfg.SpaceID, "err", err)
		}
	}
}

// reportUnreadable tells the operator that stored records cannot be decoded.
//
// A Space whose configuration is corrupt drops out of the schedule and out of
// this sweep — it is not stale, it is invisible, and nobody would ever be told.
// The count is all the operator gets: the document's key contains the space id,
// and an operator event must not name a Space (decisions.md #15). The log line
// the scheduler writes carries the key, for whoever has the log.
func (m *Monitor) reportUnreadable(ctx context.Context, count int, now time.Time) {
	if count == 0 {
		return
	}
	m.logger.Error("state documents could not be read", "count", count)

	notified, err := m.notifiedRecentlyToOperator(ctx, KindStateUnreadable, now)
	if err != nil {
		m.logger.Error("could not read operator notification history", "err", err)
		return
	}
	if notified {
		return
	}
	msg := fmt.Sprintf("%d stored record(s) could not be read. "+
		"Spaces whose configuration is affected are not being backed up.", count)
	if _, err := m.notifier.OperatorEvent(ctx, KindStateUnreadable, msg); err != nil {
		m.logger.Error("could not record unreadable-state notification", "err", err)
	}
}

// isStale reports whether a Space has gone too long without a successful
// backup, and since when.
func (m *Monitor) isStale(ctx context.Context, cfg spacecfg.Config, now time.Time) (bool, time.Time, error) {
	// Only backups can answer this, and only backups are read: a Space that
	// prunes and restores as well would otherwise crowd its own last successful
	// backup out of the window and be reported stale while it is healthy.
	history, err := m.jobs.ListRecentOfKind(ctx, cfg.SpaceID, jobs.KindBackup, historyLookback)
	if err != nil {
		return false, time.Time{}, fmt.Errorf("notify: read run history: %w", err)
	}

	since := cfg.CreatedAt
	if last, ok := jobs.LastOf(history, jobs.KindBackup, jobs.StateSucceeded); ok {
		since = last.CreatedAt
	}
	if since.IsZero() {
		// Nothing to measure against yet.
		return false, time.Time{}, nil
	}

	return now.Sub(since) > m.staleAfter(cfg, since), since, nil
}

// staleAfter derives the threshold from the Space's own schedule.
func (m *Monitor) staleAfter(cfg spacecfg.Config, from time.Time) time.Duration {
	threshold := m.opts.MinStaleAfter
	if interval, ok := scheduleInterval(cfg.EffectiveSchedule(), from); ok {
		if scaled := staleFactor * interval; scaled > threshold {
			threshold = scaled
		}
	}
	return threshold
}

// notifiedRecently reports whether this Space was already told it is stale
// within the repeat window.
func (m *Monitor) notifiedRecently(ctx context.Context, spaceID string, now time.Time) (bool, error) {
	events, err := m.events.List(ctx, spaceID, historyLookback)
	if err != nil {
		return false, err
	}
	for _, e := range events {
		if e.Kind != KindBackupStale {
			continue
		}
		return now.Sub(e.CreatedAt) < m.opts.RepeatAfter, nil
	}
	return false, nil
}

// notifiedRecentlyToOperator reports whether the operator was already told
// about this kind of problem within the repeat window.
func (m *Monitor) notifiedRecentlyToOperator(ctx context.Context, kind Kind, now time.Time) (bool, error) {
	events, err := m.events.ListOperator(ctx, historyLookback)
	if err != nil {
		return false, err
	}
	for _, e := range events {
		if e.Kind != kind {
			continue
		}
		return now.Sub(e.CreatedAt) < m.opts.RepeatAfter, nil
	}
	return false, nil
}

// scheduleInterval measures the gap between two consecutive occurrences.
func scheduleInterval(expr string, from time.Time) (time.Duration, bool) {
	first, err := scheduler.NextAfter(expr, from)
	if err != nil || first.IsZero() {
		return 0, false
	}
	second, err := scheduler.NextAfter(expr, first)
	if err != nil || second.IsZero() {
		return 0, false
	}
	interval := second.Sub(first)
	if interval <= 0 {
		return 0, false
	}
	return interval, true
}

// staleMessage is the text a member sees. It names a date and nothing else.
func staleMessage(since time.Time) string {
	return fmt.Sprintf("This space has had no successful backup since %s. Check its backup settings.",
		since.UTC().Format("2 January 2006"))
}
