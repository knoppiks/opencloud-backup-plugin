// Package spacecfg holds the per-Space backup configuration: which target a
// Space backs up to and how deep its retention window is.
//
// This is the user-owned counterpart to pkg/targets, which is admin-owned. An
// admin creates targets and grants them (decisions.md #12/#15); a Space member
// then binds their Space to one of the targets granted to them. Keeping the two
// stores apart keeps the trust boundary legible: nothing here lets an admin
// reach a user's data, and nothing here is trusted from client input — the
// binding is validated against targets.Authorizer before it is stored.
//
// Retention is always time-based (decisions.md #10); RetentionWindow is a
// keep-within duration, never a snapshot count. Phase 6 added the schedule to
// the same record, so "when does this Space back up" survives a restart along
// with "where to".
package spacecfg

import (
	"context"
	"time"
)

// DefaultRetentionWindow is the deep, time-based default: retention depth — not
// WORM — is what defeats slow-burn ransomware (decisions.md threat model).
const DefaultRetentionWindow = 90 * 24 * time.Hour

// DefaultSchedule is what an enabled Space backs up on when nobody picked a
// time: nightly at 02:30, in the service's timezone. The product promise is
// "one click, then forget" (decisions.md, product framing), so enabling backup
// must not require a second decision about scheduling.
const DefaultSchedule = "30 2 * * *"

// Config is one Space's backup configuration.
type Config struct {
	// SpaceID is the CS3 space id; one config per Space (decisions.md #6).
	SpaceID string `json:"space_id"`
	// TargetID is the admin-managed target this Space backs up to. It is only
	// ever stored after a server-side grant check.
	TargetID string `json:"target_id"`
	// RetentionWindow is the time-based keep-within window applied by the prune
	// job (decisions.md #10). Zero means DefaultRetentionWindow.
	RetentionWindow time.Duration `json:"retention_window"`
	// Schedule is the cron expression the scheduler runs this Space on. Empty
	// means DefaultSchedule. It is stored as cron even when the UI offered a
	// preset, so there is exactly one representation to reason about.
	Schedule string `json:"schedule,omitempty"`
	// Enabled reports whether scheduled runs should happen. Manual runs are
	// still possible while disabled.
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// EffectiveRetentionWindow resolves the configured window, applying the default.
func (c Config) EffectiveRetentionWindow() time.Duration {
	if c.RetentionWindow <= 0 {
		return DefaultRetentionWindow
	}
	return c.RetentionWindow
}

// EffectiveSchedule resolves the configured schedule, applying the default.
func (c Config) EffectiveSchedule() string {
	if c.Schedule == "" {
		return DefaultSchedule
	}
	return c.Schedule
}

// Store persists per-Space backup configuration.
type Store interface {
	// Get returns a Space's configuration or ErrNotFound.
	Get(ctx context.Context, spaceID string) (Config, error)
	// Put creates or replaces a Space's configuration.
	Put(ctx context.Context, c Config) (Config, error)
	// Delete removes a Space's configuration. Deleting a configuration never
	// touches the snapshots already on the target.
	Delete(ctx context.Context, spaceID string) error
	// List returns every configured Space (used by the Phase-6 scheduler).
	List(ctx context.Context) ([]Config, error)
}

// ErrNotFound is returned when a Space has no backup configuration.
type ErrNotFound struct{ SpaceID string }

func (e ErrNotFound) Error() string {
	return "spacecfg: no backup configuration for space " + e.SpaceID
}
