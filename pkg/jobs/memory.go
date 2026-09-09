package jobs

// In-memory Store + Locker: the reference implementation and the test double.
//
// The service runs the state-backed store (state.go) in production, because a
// scheduler whose history dies with the process cannot tell a missed run from a
// fresh install. This implementation stays because it is the fastest possible
// double for the runner and API tests, and because it defines the behaviour the
// persistent store is tested against.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore is a concurrency-safe in-memory Store and Locker.
type MemoryStore struct {
	clock Clock

	mu   sync.RWMutex
	jobs map[string]Job
	// held is the set of space ids with an active run.
	held map[string]struct{}
}

var (
	_ Store  = (*MemoryStore)(nil)
	_ Locker = (*MemoryStore)(nil)
)

// NewMemoryStore constructs an empty MemoryStore using the wall clock.
func NewMemoryStore() *MemoryStore { return NewMemoryStoreWithClock(systemClock{}) }

// NewMemoryStoreWithClock constructs an empty MemoryStore with an injected clock.
func NewMemoryStoreWithClock(clock Clock) *MemoryStore {
	if clock == nil {
		clock = systemClock{}
	}
	return &MemoryStore{
		clock: clock,
		jobs:  make(map[string]Job),
		held:  make(map[string]struct{}),
	}
}

// Create records a new job, generating an id and timestamps when absent.
func (m *MemoryStore) Create(_ context.Context, j Job) (Job, error) {
	j, err := prepare(j, m.clock.Now())
	if err != nil {
		return Job{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.jobs[j.ID]; exists {
		return Job{}, fmt.Errorf("jobs: duplicate job id")
	}
	m.jobs[j.ID] = j
	return j, nil
}

// Get returns one job by id.
func (m *MemoryStore) Get(_ context.Context, id string) (Job, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, ErrNotFound{ID: id}
	}
	return j, nil
}

// List returns a Space's jobs, newest first.
func (m *MemoryStore) List(ctx context.Context, spaceID string) ([]Job, error) {
	return m.ListRecent(ctx, spaceID, 0)
}

// ListRecent returns at most limit of a Space's jobs, newest first.
func (m *MemoryStore) ListRecent(_ context.Context, spaceID string, limit int) ([]Job, error) {
	return m.list(spaceID, "", limit), nil
}

// ListRecentOfKind returns at most limit of a Space's jobs of one kind, newest
// first.
func (m *MemoryStore) ListRecentOfKind(_ context.Context, spaceID string, kind Kind, limit int) ([]Job, error) {
	return m.list(spaceID, kind, limit), nil
}

// list collects a Space's jobs, optionally of one kind. An empty kind matches
// every kind.
func (m *MemoryStore) list(spaceID string, kind Kind, limit int) []Job {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		if j.SpaceID != spaceID {
			continue
		}
		if kind != "" && j.Kind != kind {
			continue
		}
		out = append(out, j)
	}
	sortNewestFirst(out)
	return applyLimit(out, limit)
}

// ListRunning returns every job that never reached a terminal state, newest
// first.
func (m *MemoryStore) ListRunning(_ context.Context) ([]Job, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		if !j.State.Terminal() {
			out = append(out, j)
		}
	}
	sortNewestFirst(out)
	return out, nil
}

// Finish records a terminal state and the run's outcome.
func (m *MemoryStore) Finish(_ context.Context, id string, out Outcome) error {
	if !out.State.Terminal() {
		return ErrNotTerminal
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return ErrNotFound{ID: id}
	}
	m.jobs[id] = applyOutcome(j, out, m.clock.Now())
	return nil
}

// PruneBefore deletes finished jobs created before cutoff.
func (m *MemoryStore) PruneBefore(_ context.Context, cutoff time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	removed := 0
	for id, j := range m.jobs {
		if prunable(j, cutoff) {
			delete(m.jobs, id)
			removed++
		}
	}
	return removed, nil
}

// Acquire takes the Space's run lock. This lock is process-local and has no
// lease: a MemoryStore dies with the process, so there is nothing to recover.
func (m *MemoryStore) Acquire(_ context.Context, spaceID string) (func(), error) {
	if spaceID == "" {
		return nil, fmt.Errorf("jobs: space id required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, held := m.held[spaceID]; held {
		return nil, ErrLocked
	}
	m.held[spaceID] = struct{}{}

	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			delete(m.held, spaceID)
		})
	}, nil
}

// --- shared helpers, used by both Store implementations --------------------

// prepare validates and completes a new job record.
func prepare(j Job, now time.Time) (Job, error) {
	if j.SpaceID == "" {
		return Job{}, fmt.Errorf("jobs: space id required")
	}
	if j.Kind == "" {
		return Job{}, fmt.Errorf("jobs: kind required")
	}
	if j.State == "" {
		j.State = StatePending
	}
	if j.Trigger == "" {
		j.Trigger = TriggerManual
	}
	if j.ID == "" {
		id, err := newID()
		if err != nil {
			return Job{}, err
		}
		j.ID = id
	}
	j.CreatedAt, j.UpdatedAt = now, now
	return j, nil
}

// applyOutcome writes a terminal outcome onto a job record.
func applyOutcome(j Job, out Outcome, now time.Time) Job {
	j.State = out.State
	j.Error = out.Error
	if out.SnapshotID != "" {
		j.SnapshotID = out.SnapshotID
	}
	if out.FileCount > 0 {
		j.FileCount = out.FileCount
	}
	if out.TotalBytes > 0 {
		j.TotalBytes = out.TotalBytes
	}
	if out.SnapshotsDeleted > 0 {
		j.SnapshotsDeleted = out.SnapshotsDeleted
	}
	if out.SnapshotsKept > 0 {
		j.SnapshotsKept = out.SnapshotsKept
	}
	j.UpdatedAt = now
	j.FinishedAt = now
	return j
}

// prunable reports whether a job is old enough and finished enough to drop.
func prunable(j Job, cutoff time.Time) bool {
	return j.State.Terminal() && j.CreatedAt.Before(cutoff)
}

// sortNewestFirst orders jobs by creation time descending, id breaking ties so
// listings are stable.
func sortNewestFirst(list []Job) {
	sort.Slice(list, func(i, k int) bool {
		if list[i].CreatedAt.Equal(list[k].CreatedAt) {
			return list[i].ID < list[k].ID
		}
		return list[i].CreatedAt.After(list[k].CreatedAt)
	})
}

func applyLimit(list []Job, limit int) []Job {
	if limit > 0 && len(list) > limit {
		return list[:limit]
	}
	return list
}

// newID returns a random, opaque job id.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("jobs: generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
