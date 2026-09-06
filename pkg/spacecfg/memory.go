package spacecfg

// In-memory reference Store. It is the implementation the service wires today;
// a persistent store lands with the scheduler phase, which is when configuration
// must survive a restart.

import (
	"context"
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

// MemoryStore is a concurrency-safe in-memory Store.
type MemoryStore struct {
	clock Clock

	mu      sync.RWMutex
	configs map[string]Config
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore constructs an empty MemoryStore using the wall clock.
func NewMemoryStore() *MemoryStore { return NewMemoryStoreWithClock(systemClock{}) }

// NewMemoryStoreWithClock constructs an empty MemoryStore with an injected clock.
func NewMemoryStoreWithClock(clock Clock) *MemoryStore {
	if clock == nil {
		clock = systemClock{}
	}
	return &MemoryStore{clock: clock, configs: make(map[string]Config)}
}

// Get returns a Space's configuration.
func (m *MemoryStore) Get(_ context.Context, spaceID string) (Config, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.configs[spaceID]
	if !ok {
		return Config{}, ErrNotFound{SpaceID: spaceID}
	}
	return c, nil
}

// Put creates or replaces a Space's configuration, preserving CreatedAt.
func (m *MemoryStore) Put(_ context.Context, c Config) (Config, error) {
	if err := validate(c); err != nil {
		return Config{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.clock.Now()
	c.UpdatedAt = now
	if existing, ok := m.configs[c.SpaceID]; ok {
		c.CreatedAt = existing.CreatedAt
	} else {
		c.CreatedAt = now
	}
	m.configs[c.SpaceID] = c
	return c, nil
}

// Delete removes a Space's configuration.
func (m *MemoryStore) Delete(_ context.Context, spaceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.configs[spaceID]; !ok {
		return ErrNotFound{SpaceID: spaceID}
	}
	delete(m.configs, spaceID)
	return nil
}

// validate rejects a configuration that could not be acted on. It is shared by
// both Store implementations so they accept exactly the same records.
func validate(c Config) error {
	if c.SpaceID == "" {
		return fmt.Errorf("spacecfg: space id required")
	}
	if c.TargetID == "" {
		return fmt.Errorf("spacecfg: target id required")
	}
	if c.RetentionWindow < 0 {
		return fmt.Errorf("spacecfg: retention window must not be negative")
	}
	return nil
}

// List returns every configuration, ordered by space id for determinism.
func (m *MemoryStore) List(_ context.Context) ([]Config, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Config, 0, len(m.configs))
	for _, c := range m.configs {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SpaceID < out[j].SpaceID })
	return out, nil
}
