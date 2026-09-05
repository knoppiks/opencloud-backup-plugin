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
// keep-within duration, never a snapshot count. Phase 6 adds the cron schedule
// to the same record.
package spacecfg

import (
	"context"
	"time"
)

// DefaultRetentionWindow is the deep, time-based default: retention depth — not
// WORM — is what defeats slow-burn ransomware (decisions.md threat model).
const DefaultRetentionWindow = 90 * 24 * time.Hour

// Config is one Space's backup configuration.
type Config struct {
	// SpaceID is the CS3 space id; one config per Space (decisions.md #6).
	SpaceID string
	// TargetID is the admin-managed target this Space backs up to. It is only
	// ever stored after a server-side grant check.
	TargetID string
	// RetentionWindow is the time-based keep-within window applied by the prune
	// job (decisions.md #10). Zero means DefaultRetentionWindow.
	RetentionWindow time.Duration
	// Enabled reports whether scheduled runs should happen. Manual runs are
	// still possible while disabled.
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// EffectiveRetentionWindow resolves the configured window, applying the default.
func (c Config) EffectiveRetentionWindow() time.Duration {
	if c.RetentionWindow <= 0 {
		return DefaultRetentionWindow
	}
	return c.RetentionWindow
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
