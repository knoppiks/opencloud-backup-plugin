package notify

// The staleness rule, in one place.
//
// Two surfaces answer "has this Space gone stale": the monitor, which notifies
// members, and the status endpoint, which colours the card on the member's
// screen. If they disagreed, the card could be green while the notification
// said otherwise, or the reverse. So both call StaleRule.Assess, and neither
// computes staleness itself.

import (
	"time"

	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/spacecfg"
)

// StaleLookback is how many of a Space's most recent backup records a caller
// must hand to Assess. It is a contract, not a tuning knob: two callers passing
// different windows could disagree about when the last success was.
const StaleLookback = 50

// StaleRule decides whether a Space's backups have gone stale. The zero value
// applies DefaultMinStaleAfter.
type StaleRule struct {
	// MinStaleAfter is the floor on the threshold; zero or less uses the
	// default.
	MinStaleAfter time.Duration
}

// Staleness is the rule's verdict.
type Staleness struct {
	// Stale reports whether the Space has gone too long without a successful
	// backup.
	Stale bool
	// Since is the last successful backup or, if there has never been one,
	// when the Space was configured. Zero when there is nothing to measure
	// from.
	Since time.Time
}

// Assess applies the rule. backups must be the Space's backup-kind history,
// newest first, read with a limit of StaleLookback.
//
// A Space whose scheduled backups are switched off is not stale: it is off,
// and saying otherwise would be a warning nobody can clear except by turning
// backups on.
func (r StaleRule) Assess(cfg spacecfg.Config, backups []jobs.Job, now time.Time) Staleness {
	if !cfg.Enabled {
		return Staleness{}
	}
	since := cfg.CreatedAt
	if last, ok := jobs.LastOf(backups, jobs.KindBackup, jobs.StateSucceeded); ok {
		since = last.CreatedAt
	}
	if since.IsZero() {
		// Nothing to measure against yet.
		return Staleness{}
	}
	return Staleness{
		Stale: now.Sub(since) > r.threshold(cfg, since),
		Since: since,
	}
}

// threshold derives the allowed gap from the Space's own schedule, so a nightly
// Space is stale after two missed nights and a weekly one after two missed
// weeks.
func (r StaleRule) threshold(cfg spacecfg.Config, from time.Time) time.Duration {
	threshold := r.MinStaleAfter
	if threshold <= 0 {
		threshold = DefaultMinStaleAfter
	}
	if interval, ok := scheduleInterval(cfg.EffectiveSchedule(), from); ok {
		if scaled := staleFactor * interval; scaled > threshold {
			threshold = scaled
		}
	}
	return threshold
}
