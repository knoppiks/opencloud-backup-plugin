// In-memory Store + Authorizer for the target/grant model. The service runs the
// state-backed store (state.go); this stays as the test double and the behaviour
// reference the contract tests hold both implementations to.
//
// It upholds the least-disclosure rule: VisibleTargets returns only PublicView
// projections ({id,name}); credentials never leave the store (decisions.md
// #12/#14). Grant evaluation is entirely server-side — the caller passes their
// authenticated subject and the spaces they belong to (derived from CS3
// membership), never a client-supplied target id.
package targets

import (
	"context"
	"sync"
)

// MemoryStore is a concurrency-safe in-memory Store and Authorizer.
type MemoryStore struct {
	mu      sync.RWMutex
	targets map[string]Target
	grants  map[string][]Grant // keyed by target id
}

// NewMemoryStore constructs an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		targets: make(map[string]Target),
		grants:  make(map[string][]Grant),
	}
}

var (
	_ Store      = (*MemoryStore)(nil)
	_ Authorizer = (*MemoryStore)(nil)
)

// CreateTarget persists a target (WrappedCreds already sealed by the caller).
func (m *MemoryStore) CreateTarget(_ context.Context, t Target) (Target, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.targets[t.ID] = t
	return t, nil
}

// UpdateTarget updates metadata; nil WrappedCreds preserves stored credentials.
func (m *MemoryStore) UpdateTarget(_ context.Context, t Target) (Target, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.targets[t.ID]
	if !ok {
		return Target{}, ErrNotFound{ID: t.ID}
	}
	if t.WrappedCreds == nil {
		t.WrappedCreds = existing.WrappedCreds
		t.Version = existing.Version
	}
	m.targets[t.ID] = t
	return t, nil
}

// DeleteTarget removes a target and its grants.
func (m *MemoryStore) DeleteTarget(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.targets[id]; !ok {
		return ErrNotFound{ID: id}
	}
	delete(m.targets, id)
	delete(m.grants, id)
	return nil
}

// GetTarget returns a target by id (including WrappedCreds for worker use).
func (m *MemoryStore) GetTarget(_ context.Context, id string) (Target, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.targets[id]
	if !ok {
		return Target{}, ErrNotFound{ID: id}
	}
	return t, nil
}

// ListTargets returns all targets (admin view).
func (m *MemoryStore) ListTargets(_ context.Context) ([]Target, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Target, 0, len(m.targets))
	for _, t := range m.targets {
		out = append(out, t)
	}
	return out, nil
}

// PutGrant adds or replaces a grant.
func (m *MemoryStore) PutGrant(_ context.Context, g Grant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.grants[g.TargetID]
	for i, existing := range list {
		if grantEqual(existing, g) {
			list[i] = g
			m.grants[g.TargetID] = list
			return nil
		}
	}
	m.grants[g.TargetID] = append(list, g)
	return nil
}

// DeleteGrant removes a grant.
func (m *MemoryStore) DeleteGrant(_ context.Context, g Grant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.grants[g.TargetID]
	for i, existing := range list {
		if grantEqual(existing, g) {
			m.grants[g.TargetID] = append(list[:i], list[i+1:]...)
			return nil
		}
	}
	return nil
}

// ListGrants returns the grants for a target.
func (m *MemoryStore) ListGrants(_ context.Context, targetID string) ([]Grant, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := m.grants[targetID]
	out := make([]Grant, len(list))
	copy(out, list)
	return out, nil
}

// VisibleTargets returns least-disclosure projections of the targets granted to
// the user, given the spaces they are a member of (server-side check).
func (m *MemoryStore) VisibleTargets(_ context.Context, userSub string, spaceIDs []string) ([]PublicView, error) {
	spaceSet := make(map[string]struct{}, len(spaceIDs))
	for _, id := range spaceIDs {
		spaceSet[id] = struct{}{}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]PublicView, 0)
	for id, t := range m.targets {
		if grantsAllow(m.grants[id], userSub, spaceSet) {
			out = append(out, t.Public())
		}
	}
	return out, nil
}

// MayUse reports whether the user may use targetID for spaceID. All checks are
// server-side; a client-supplied targetID that is not granted returns false.
func (m *MemoryStore) MayUse(_ context.Context, userSub, spaceID, targetID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.targets[targetID]; !ok {
		return false, ErrNotFound{ID: targetID}
	}
	spaceSet := map[string]struct{}{}
	if spaceID != "" {
		spaceSet[spaceID] = struct{}{}
	}
	return grantsAllow(m.grants[targetID], userSub, spaceSet), nil
}

// grantsAllow evaluates whether any grant admits userSub given their spaces.
func grantsAllow(grants []Grant, userSub string, spaceSet map[string]struct{}) bool {
	for _, g := range grants {
		switch g.Scope {
		case ScopeAllUsers:
			return true
		case ScopeUser:
			if g.UserSub == userSub {
				return true
			}
		case ScopeSpace:
			if _, ok := spaceSet[g.SpaceID]; ok {
				return true
			}
		}
	}
	return false
}

func grantEqual(a, b Grant) bool {
	return a.TargetID == b.TargetID &&
		a.Scope == b.Scope &&
		a.UserSub == b.UserSub &&
		a.SpaceID == b.SpaceID
}
