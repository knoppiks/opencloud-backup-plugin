package state

// In-memory Store. It is the reference implementation the unit tests exercise,
// and the fallback the service runs with when no durable backend is configured
// (in which case schedules and history do not survive a restart — the service
// says so at startup rather than pretending otherwise).

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// MemoryStore is a concurrency-safe in-memory Store.
type MemoryStore struct {
	mu     sync.RWMutex
	values map[string][]byte
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{values: make(map[string][]byte)}
}

// Get returns a copy of the stored value.
func (m *MemoryStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.values[key]
	if !ok {
		return nil, ErrNotFound{Key: key}
	}
	return append([]byte(nil), v...), nil
}

// Create stores a copy of value, refusing to replace an existing one.
func (m *MemoryStore) Create(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.values[key]; exists {
		return ErrExists{Key: key}
	}
	m.values[key] = append([]byte(nil), value...)
	return nil
}

// Replace stores a copy of value, discarding any previous one.
func (m *MemoryStore) Replace(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[key] = append([]byte(nil), value...)
	return nil
}

// Delete removes a key.
func (m *MemoryStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.values[key]; !ok {
		return ErrNotFound{Key: key}
	}
	delete(m.values, key)
	return nil
}

// List returns the keys under prefix, matched on whole segments.
func (m *MemoryStore) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []string
	for k := range m.values {
		if hasSegmentPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

// hasSegmentPrefix reports whether key lies below prefix on a segment boundary,
// so that listing "a/b" never returns "a/bc".
func hasSegmentPrefix(key, prefix string) bool {
	if prefix == "" {
		return true
	}
	prefix = strings.TrimSuffix(prefix, "/")
	return key == prefix || strings.HasPrefix(key, prefix+"/")
}
