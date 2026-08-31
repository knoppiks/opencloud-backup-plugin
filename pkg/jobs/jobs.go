// Package jobs defines the job/state store: a record per backup/prune/restore
// run and its lifecycle state, plus the per-Space lock that keeps two runs for
// the same Space from overlapping.
//
// Phase 4 needs a minimal record and a working lock; Phase 6 fleshes the store
// out (history, notifications, persistence).
package jobs

import (
	"context"
	"errors"
	"time"
)

// Kind is the kind of work a job represents.
type Kind string

const (
	KindBackup  Kind = "backup"
	KindPrune   Kind = "prune"
	KindRestore Kind = "restore"
)

// State is a job's lifecycle state.
type State string

const (
	StatePending   State = "pending"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
)

// Terminal reports whether no further transition is expected.
func (s State) Terminal() bool { return s == StateSucceeded || s == StateFailed }

// Job is one unit of tracked work for a Space.
type Job struct {
	ID        string
	SpaceID   string
	Kind      Kind
	State     State
	CreatedAt time.Time
	UpdatedAt time.Time
	// Error is a sanitized, user-safe message; it must never contain key
	// material or internal CS3/S3 details (AGENTS.md error rules).
	Error string
	// SnapshotID is the snapshot a successful backup produced. Empty otherwise.
	SnapshotID string
}

// Store persists jobs and their state transitions.
type Store interface {
	Create(ctx context.Context, j Job) (Job, error)
	Get(ctx context.Context, id string) (Job, error)
	List(ctx context.Context, spaceID string) ([]Job, error)
	UpdateState(ctx context.Context, id string, state State, errMsg string) error
	// SetSnapshotID records the snapshot a backup produced.
	SetSnapshotID(ctx context.Context, id, snapshotID string) error
}

// ErrLocked is returned when another run already holds a Space's lock.
var ErrLocked = errors.New("jobs: another run is already in progress for this space")

// ErrNotFound is returned when no job exists with the given id.
type ErrNotFound struct{ ID string }

func (e ErrNotFound) Error() string { return "jobs: no job with id " + e.ID }

// Locker serialises runs per Space. Concurrent backup runs against one kopia
// repository would race on maintenance and produce confusing partial history,
// so a Space runs at most one job at a time.
type Locker interface {
	// Acquire takes the Space's lock, returning ErrLocked if it is held. The
	// returned release function is safe to call more than once.
	Acquire(ctx context.Context, spaceID string) (release func(), err error)
}
