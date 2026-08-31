package keys

// In-memory keys.Store. It holds only wrapped envelopes — no plaintext key
// material ever enters this store. A durable store (file/DB) can be swapped in
// behind the same interface; every consumer depends on keys.Store, not on this
// type.

import "sync"

// MemoryStore is a concurrency-safe in-memory Store.
type MemoryStore struct {
	mu    sync.RWMutex
	byID  map[string]*SpaceKeys
	clock Clock
}

// NewMemoryStore constructs an empty MemoryStore using the system clock.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byID: make(map[string]*SpaceKeys), clock: systemClock{}}
}

// NewMemoryStoreWithClock constructs a MemoryStore with an injected clock.
func NewMemoryStoreWithClock(c Clock) *MemoryStore {
	if c == nil {
		c = systemClock{}
	}
	return &MemoryStore{byID: make(map[string]*SpaceKeys), clock: c}
}

var _ Store = (*MemoryStore)(nil)

// record returns (creating if needed) the mutable record for a space.
// Caller must hold the write lock.
func (m *MemoryStore) record(spaceID string) *SpaceKeys {
	rec, ok := m.byID[spaceID]
	if !ok {
		now := m.clock.Now().UTC()
		rec = &SpaceKeys{SpaceID: spaceID, CreatedAt: now, UpdatedAt: now}
		m.byID[spaceID] = rec
	}
	return rec
}

// PutSRW stores the SRW-wrapped DK for a space.
func (m *MemoryStore) PutSRW(spaceID string, w WrappedDK) error {
	if w.Kind != WrapSRW {
		return ErrBadEnvelope
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.record(spaceID)
	rec.SRW = cloneWrapped(w)
	rec.UpdatedAt = m.clock.Now().UTC()
	return nil
}

// GetSRW returns the SRW-wrapped DK for a space.
func (m *MemoryStore) GetSRW(spaceID string) (WrappedDK, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.byID[spaceID]
	if !ok || rec.SRW.Kind != WrapSRW {
		return WrappedDK{}, ErrNotFound{SpaceID: spaceID}
	}
	return cloneWrapped(rec.SRW), nil
}

// PutRK stores the RK-wrapped DK for a space.
func (m *MemoryStore) PutRK(spaceID string, w WrappedDK) error {
	if w.Kind != WrapRK {
		return ErrBadEnvelope
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.record(spaceID)
	rec.RK = cloneWrapped(w)
	rec.UpdatedAt = m.clock.Now().UTC()
	return nil
}

// GetRK returns the RK-wrapped DK for a space.
func (m *MemoryStore) GetRK(spaceID string) (WrappedDK, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.byID[spaceID]
	if !ok || rec.RK.Kind != WrapRK {
		return WrappedDK{}, ErrNotFound{SpaceID: spaceID}
	}
	return cloneWrapped(rec.RK), nil
}

// Status reports setup state for a space without revealing key material.
func (m *MemoryStore) Status(spaceID string) (Status, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.byID[spaceID]
	if !ok {
		return Status{SpaceID: spaceID}, nil
	}
	hasRK := rec.RK.Kind == WrapRK
	hasSRW := rec.SRW.Kind == WrapSRW
	return Status{
		SpaceID:    spaceID,
		Configured: hasRK && hasSRW,
		HasRK:      hasRK,
		HasSRW:     hasSRW,
		RKVersion:  rec.RK.Version,
		SRWVersion: rec.SRW.Version,
		CreatedAt:  rec.CreatedAt,
		UpdatedAt:  rec.UpdatedAt,
	}, nil
}

// Delete removes a space's key record. Used by tests and teardown; it does not
// destroy snapshot data.
func (m *MemoryStore) Delete(spaceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byID, spaceID)
}

// cloneWrapped copies an envelope so callers cannot mutate stored state.
func cloneWrapped(w WrappedDK) WrappedDK {
	out := w
	if w.Blob != nil {
		out.Blob = make([]byte, len(w.Blob))
		copy(out.Blob, w.Blob)
	}
	return out
}
