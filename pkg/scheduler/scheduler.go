// Package scheduler runs per-Space cron scheduling for backup and prune jobs.
// Prune is scheduled as a separate job from backup (decisions.md #9 Tier 1).
// Implemented in Phase 6. This file defines the boundary interfaces only.
//
// Time is injected via Clock so schedule logic is deterministically testable
// (AGENTS.md testability rule). A fake Clock lives in internal/testutil.
package scheduler

import (
	"context"
	"time"
)

// Clock abstracts time for deterministic scheduler tests.
type Clock interface {
	Now() time.Time
}

// Schedule is a per-Space cron schedule.
type Schedule struct {
	SpaceID string
	// Cron is a standard cron expression for backup runs.
	Cron string
	// RetentionWindow is the time-based keep-within window for prune
	// (decisions.md #10).
	RetentionWindow time.Duration
}

// Scheduler owns registered schedules and dispatches due jobs.
type Scheduler interface {
	// Add registers or replaces a space's schedule.
	Add(ctx context.Context, s Schedule) error
	// Remove deregisters a space's schedule.
	Remove(ctx context.Context, spaceID string) error
	// Run blocks, dispatching due jobs until ctx is cancelled.
	Run(ctx context.Context) error
}

// systemClock is the production Clock.
type systemClock struct{}

// SystemClock returns a Clock backed by the wall clock.
func SystemClock() Clock { return systemClock{} }

func (systemClock) Now() time.Time { return time.Now() }
