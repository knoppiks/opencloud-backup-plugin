// Package jobs defines the job/state store: a record per backup/prune/restore
// run and its lifecycle state. Implemented in Phase 6. This file defines the
// boundary interface and value types only.
package jobs

import (
	"context"
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
}

// Store persists jobs and their state transitions.
type Store interface {
	Create(ctx context.Context, j Job) (Job, error)
	Get(ctx context.Context, id string) (Job, error)
	List(ctx context.Context, spaceID string) ([]Job, error)
	UpdateState(ctx context.Context, id string, state State, errMsg string) error
}
