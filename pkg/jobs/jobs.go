// Package jobs defines the job/state store: a record per backup/restore/prune
// run and its lifecycle state, plus the per-Space lock that keeps two runs for
// the same Space from overlapping.
//
// Phase 6 turned this from a Phase-4 sketch into the service's memory: run
// history survives a restart, the lock is a lease that a crashed process cannot
// hold forever, and history is pruned on a time window like everything else in
// this codebase.
//
// A job record is metadata only — ids, states, counts, timestamps and a
// sanitized error string. It never holds key material, credentials, file names
// or paths (AGENTS.md).
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

// Trigger records what started a run. It is what lets the status board say
// "your last backup ran on schedule" and lets the scheduler tell its own runs
// apart from a member pressing "back up now".
type Trigger string

const (
	// TriggerManual is a run a user asked for through the API.
	TriggerManual Trigger = "manual"
	// TriggerSchedule is an unattended run the scheduler started.
	TriggerSchedule Trigger = "schedule"
)

// Job is one unit of tracked work for a Space.
type Job struct {
	ID      string  `json:"id"`
	SpaceID string  `json:"space_id"`
	Kind    Kind    `json:"kind"`
	State   State   `json:"state"`
	Trigger Trigger `json:"trigger,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// FinishedAt is set when the job reaches a terminal state.
	FinishedAt time.Time `json:"finished_at,omitzero"`

	// FileCount and TotalBytes are the logical (pre-dedup) counts the run
	// processed. They are recorded when the run finishes; live progress during a
	// run is not tracked (see the Phase 6 amendment in decisions.md).
	FileCount  int64 `json:"file_count,omitempty"`
	TotalBytes int64 `json:"total_bytes,omitempty"`

	// Error is a sanitized, user-safe message; it must never contain key
	// material or internal CS3/S3 details (AGENTS.md error rules).
	Error string `json:"error,omitempty"`
	// SnapshotID is the snapshot a successful backup produced. Empty otherwise.
	SnapshotID string `json:"snapshot_id,omitempty"`
}

// Duration reports how long a finished job took. It is zero while running.
func (j Job) Duration() time.Duration {
	if j.FinishedAt.IsZero() {
		return 0
	}
	return j.FinishedAt.Sub(j.CreatedAt)
}

// Outcome is everything a finishing run records in one write. Terminal
// transitions are a single store operation on purpose: the durable backend
// offers no transactions, so a run must not be able to leave a half-updated
// record behind (a "succeeded" job with no snapshot id, say).
type Outcome struct {
	// State must be terminal.
	State State
	// Error is the sanitized message shown to the user. Empty on success.
	Error string
	// SnapshotID is the snapshot a backup produced, if any.
	SnapshotID string
	FileCount  int64
	TotalBytes int64
}

// Store persists jobs and their state transitions.
type Store interface {
	// Create records a new job, filling in id and timestamps when absent.
	Create(ctx context.Context, j Job) (Job, error)
	// Get returns one job by id, or ErrNotFound.
	Get(ctx context.Context, id string) (Job, error)
	// List returns a Space's jobs, newest first.
	List(ctx context.Context, spaceID string) ([]Job, error)
	// ListRecent returns at most limit of a Space's jobs, newest first. A limit
	// of zero or less means "all".
	ListRecent(ctx context.Context, spaceID string, limit int) ([]Job, error)
	// ListRunning returns every job, in any Space, that never reached a
	// terminal state. Recovery uses it to find runs whose process is gone: a
	// record left at "running" makes its Space look permanently busy, and
	// nothing else in the system would ever revisit it.
	ListRunning(ctx context.Context) ([]Job, error)
	// Finish records a terminal state and the run's outcome in one write.
	Finish(ctx context.Context, id string, out Outcome) error
	// PruneBefore deletes finished jobs created before cutoff and returns how
	// many were removed. Running jobs are never pruned.
	PruneBefore(ctx context.Context, cutoff time.Time) (int, error)
}

// DefaultHistoryWindow is how long finished runs are kept. Time-based, like
// every other retention in this system (decisions.md #10).
const DefaultHistoryWindow = 365 * 24 * time.Hour

// ErrLocked is returned when another run already holds a Space's lock.
var ErrLocked = errors.New("jobs: another run is already in progress for this space")

// ErrNotTerminal is returned when a caller tries to finish a job with a
// non-terminal state.
var ErrNotTerminal = errors.New("jobs: finish requires a terminal state")

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

// Clock supplies the current time, injected so tests are deterministic.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// SystemClock returns a Clock backed by the wall clock.
func SystemClock() Clock { return systemClock{} }

// LastOf returns the newest job in list matching kind and, optionally, state.
// Pass an empty state to match any. The list is expected newest-first, as every
// Store returns it.
func LastOf(list []Job, kind Kind, state State) (Job, bool) {
	for _, j := range list {
		if j.Kind != kind {
			continue
		}
		if state != "" && j.State != state {
			continue
		}
		return j, true
	}
	return Job{}, false
}
