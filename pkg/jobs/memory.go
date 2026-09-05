package jobs

// In-memory Store + Locker. This is the implementation Phase 4 wires; Phase 6
// replaces it with a persistent store (and, if the service is ever scaled out,
// a lock that is not process-local — noted in the phase-6 plan).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Clock supplies the current time, injected so tests are deterministic.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

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
	if j.SpaceID == "" {
		return Job{}, fmt.Errorf("jobs: space id required")
	}
	if j.Kind == "" {
		return Job{}, fmt.Errorf("jobs: kind required")
	}
	if j.State == "" {
		j.State = StatePending
	}
	if j.ID == "" {
		id, err := newID()
		if err != nil {
			return Job{}, err
		}
		j.ID = id
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.jobs[j.ID]; exists {
		return Job{}, fmt.Errorf("jobs: duplicate job id")
	}

	now := m.clock.Now()
	j.CreatedAt, j.UpdatedAt = now, now
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
func (m *MemoryStore) List(_ context.Context, spaceID string) ([]Job, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		if j.SpaceID == spaceID {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].CreatedAt.Equal(out[k].CreatedAt) {
			return out[i].ID < out[k].ID
		}
		return out[i].CreatedAt.After(out[k].CreatedAt)
	})
	return out, nil
}

// UpdateState transitions a job and records a sanitized error message.
func (m *MemoryStore) UpdateState(_ context.Context, id string, state State, errMsg string) error {
	if state == "" {
		return fmt.Errorf("jobs: state required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return ErrNotFound{ID: id}
	}
	j.State = state
	j.Error = errMsg
	j.UpdatedAt = m.clock.Now()
	m.jobs[id] = j
	return nil
}

// SetSnapshotID records the snapshot a backup run produced.
func (m *MemoryStore) SetSnapshotID(_ context.Context, id, snapshotID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return ErrNotFound{ID: id}
	}
	j.SnapshotID = snapshotID
	j.UpdatedAt = m.clock.Now()
	m.jobs[id] = j
	return nil
}

// Acquire takes the Space's run lock.
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

// newID returns a random, opaque job id.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("jobs: generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
