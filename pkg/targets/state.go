package targets

// The durable Store + Authorizer, on top of pkg/state.
//
// Layout: one document per target at "targets/<id>", and one document per
// target's grant list at "targetgrants/<id>". Grants are kept as a list rather
// than a document each so that adding or removing one is a single write — the
// backend has no transactions, and a half-written grant set is an authorization
// bug, not a cosmetic one.
//
// What is persisted for a target includes WrappedCreds: TW-wrapped ciphertext,
// never plaintext (decisions.md #14). The TW key stays in the cluster secret, so
// a stolen state store yields no usable credentials. Nothing here ever returns
// or logs plaintext credentials.

import (
	"context"
	"fmt"
	"sort"

	"opencloud-backup-plugin/pkg/state"
)

const (
	// targetPrefix roots target documents.
	targetPrefix = "targets"
	// grantPrefix roots per-target grant lists.
	grantPrefix = "targetgrants"
)

// StateStore is a Store and Authorizer backed by durable state.
type StateStore struct {
	targets *state.Documents[Target]
	grants  *state.Documents[[]Grant]
}

var (
	_ Store      = (*StateStore)(nil)
	_ Authorizer = (*StateStore)(nil)
)

// NewStateStore returns a Store persisting to st.
func NewStateStore(st state.Store) *StateStore {
	return &StateStore{
		targets: state.NewDocuments[Target](st, targetPrefix),
		grants:  state.NewDocuments[[]Grant](st, grantPrefix),
	}
}

// CreateTarget persists a target (WrappedCreds already sealed by the caller).
func (s *StateStore) CreateTarget(ctx context.Context, t Target) (Target, error) {
	if t.ID == "" {
		return Target{}, fmt.Errorf("targets: target id required")
	}
	if err := s.targets.Put(ctx, t, t.ID); err != nil {
		return Target{}, fmt.Errorf("targets: store target: %w", err)
	}
	return t, nil
}

// UpdateTarget updates metadata; nil WrappedCreds preserves stored credentials,
// which is what makes "edit a target without re-entering its secret" possible
// while credentials stay write-only.
func (s *StateStore) UpdateTarget(ctx context.Context, t Target) (Target, error) {
	existing, err := s.GetTarget(ctx, t.ID)
	if err != nil {
		return Target{}, err
	}
	if t.WrappedCreds == nil {
		t.WrappedCreds = existing.WrappedCreds
		t.Version = existing.Version
	}
	if err := s.targets.Put(ctx, t, t.ID); err != nil {
		return Target{}, fmt.Errorf("targets: store target: %w", err)
	}
	return t, nil
}

// DeleteTarget removes a target and its grants.
func (s *StateStore) DeleteTarget(ctx context.Context, id string) error {
	if err := s.targets.Delete(ctx, id); err != nil {
		if state.IsNotFound(err) {
			return ErrNotFound{ID: id}
		}
		return fmt.Errorf("targets: delete target: %w", err)
	}
	// A grant that outlived its target would be a dangling permission.
	if err := s.grants.Delete(ctx, id); err != nil && !state.IsNotFound(err) {
		return fmt.Errorf("targets: delete grants: %w", err)
	}
	return nil
}

// GetTarget returns a target by id (including WrappedCreds for worker use).
func (s *StateStore) GetTarget(ctx context.Context, id string) (Target, error) {
	t, err := s.targets.Get(ctx, id)
	if err != nil {
		if state.IsNotFound(err) {
			return Target{}, ErrNotFound{ID: id}
		}
		return Target{}, fmt.Errorf("targets: read target: %w", err)
	}
	return t, nil
}

// ListTargets returns all targets (admin view).
func (s *StateStore) ListTargets(ctx context.Context) ([]Target, error) {
	out, err := s.targets.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("targets: list targets: %w", err)
	}
	return out, nil
}

// PutGrant adds or replaces a grant.
func (s *StateStore) PutGrant(ctx context.Context, g Grant) error {
	if g.TargetID == "" {
		return fmt.Errorf("targets: target id required")
	}
	list, err := s.ListGrants(ctx, g.TargetID)
	if err != nil {
		return err
	}
	for i, existing := range list {
		if grantEqual(existing, g) {
			list[i] = g
			return s.writeGrants(ctx, g.TargetID, list)
		}
	}
	return s.writeGrants(ctx, g.TargetID, append(list, g))
}

// DeleteGrant removes a grant.
func (s *StateStore) DeleteGrant(ctx context.Context, g Grant) error {
	list, err := s.ListGrants(ctx, g.TargetID)
	if err != nil {
		return err
	}
	for i, existing := range list {
		if grantEqual(existing, g) {
			return s.writeGrants(ctx, g.TargetID, append(list[:i], list[i+1:]...))
		}
	}
	return nil
}

// ListGrants returns the grants for a target.
func (s *StateStore) ListGrants(ctx context.Context, targetID string) ([]Grant, error) {
	list, err := s.grants.Get(ctx, targetID)
	if err != nil {
		if state.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("targets: read grants: %w", err)
	}
	return list, nil
}

func (s *StateStore) writeGrants(ctx context.Context, targetID string, list []Grant) error {
	if err := s.grants.Put(ctx, list, targetID); err != nil {
		return fmt.Errorf("targets: store grants: %w", err)
	}
	return nil
}

// VisibleTargets returns least-disclosure projections of the targets granted to
// the user, given the spaces they are a member of (server-side check).
func (s *StateStore) VisibleTargets(ctx context.Context, userSub string, spaceIDs []string) ([]PublicView, error) {
	all, err := s.ListTargets(ctx)
	if err != nil {
		return nil, err
	}
	spaceSet := spaceSetOf(spaceIDs)

	out := make([]PublicView, 0, len(all))
	for _, t := range all {
		grants, err := s.ListGrants(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		if grantsAllow(grants, userSub, spaceSet) {
			out = append(out, t.Public())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// MayUse reports whether the user may use targetID for spaceID. All checks are
// server-side; a client-supplied targetID that is not granted returns false.
func (s *StateStore) MayUse(ctx context.Context, userSub, spaceID, targetID string) (bool, error) {
	if _, err := s.GetTarget(ctx, targetID); err != nil {
		return false, err
	}
	grants, err := s.ListGrants(ctx, targetID)
	if err != nil {
		return false, err
	}
	var ids []string
	if spaceID != "" {
		ids = []string{spaceID}
	}
	return grantsAllow(grants, userSub, spaceSetOf(ids)), nil
}

func spaceSetOf(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}
