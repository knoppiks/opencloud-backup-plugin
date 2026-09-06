package spacecfg

// The durable Store, on top of pkg/state.
//
// Layout: one document per Space, at "spacecfg/<space-id>". A Space's
// configuration is small, singular and read on every scheduler tick, so there
// is nothing clever to do here — the whole collection is one prefix listing.

import (
	"context"
	"fmt"

	"opencloud-backup-plugin/pkg/state"
)

// configPrefix roots per-Space configuration documents.
const configPrefix = "spacecfg"

// StateStore is a Store backed by durable state.
type StateStore struct {
	docs  *state.Documents[Config]
	clock Clock
}

var _ Store = (*StateStore)(nil)

// NewStateStore returns a Store persisting to st.
func NewStateStore(st state.Store, clock Clock) *StateStore {
	if clock == nil {
		clock = systemClock{}
	}
	return &StateStore{docs: state.NewDocuments[Config](st, configPrefix), clock: clock}
}

// Get returns a Space's configuration.
func (s *StateStore) Get(ctx context.Context, spaceID string) (Config, error) {
	c, err := s.docs.Get(ctx, spaceID)
	if err != nil {
		if state.IsNotFound(err) {
			return Config{}, ErrNotFound{SpaceID: spaceID}
		}
		return Config{}, fmt.Errorf("spacecfg: read configuration: %w", err)
	}
	return c, nil
}

// Put creates or replaces a Space's configuration, preserving CreatedAt.
func (s *StateStore) Put(ctx context.Context, c Config) (Config, error) {
	if err := validate(c); err != nil {
		return Config{}, err
	}

	now := s.clock.Now()
	c.UpdatedAt = now
	c.CreatedAt = now
	if existing, err := s.docs.Get(ctx, c.SpaceID); err == nil {
		c.CreatedAt = existing.CreatedAt
	} else if !state.IsNotFound(err) {
		return Config{}, fmt.Errorf("spacecfg: read configuration: %w", err)
	}

	if err := s.docs.Put(ctx, c, c.SpaceID); err != nil {
		return Config{}, fmt.Errorf("spacecfg: store configuration: %w", err)
	}
	return c, nil
}

// Delete removes a Space's configuration.
func (s *StateStore) Delete(ctx context.Context, spaceID string) error {
	if err := s.docs.Delete(ctx, spaceID); err != nil {
		if state.IsNotFound(err) {
			return ErrNotFound{SpaceID: spaceID}
		}
		return fmt.Errorf("spacecfg: delete configuration: %w", err)
	}
	return nil
}

// List returns every configuration, ordered by space id for determinism.
func (s *StateStore) List(ctx context.Context) ([]Config, error) {
	// Documents.All returns key order, and keys are the escaped space ids, so
	// the ordering matches the memory store's.
	out, err := s.docs.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("spacecfg: list configurations: %w", err)
	}
	return out, nil
}
