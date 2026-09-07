package spacecfg

// The durable Store, on top of pkg/state.
//
// Layout (append-only, decisions.md #16): "spaceconfigs/<space-id>/<nanos>", one
// document per version, newest wins. A Space's configuration binds it to a
// target and a retention window; losing it does not lose data, but it does stop
// the Space being backed up until someone notices and re-enters it, which is a
// silent failure of exactly the kind this service exists to prevent. It costs
// nothing to keep it out of the destructive-write path with everything else that
// matters.
//
// Configurations written before versioning existed live at "spacecfg/<space-id>"
// and are read when a Space has no version yet.

import (
	"context"
	"fmt"
	"sort"

	"opencloud-backup-plugin/pkg/state"
)

const (
	// configPrefix roots the append-only per-Space configuration documents.
	configPrefix = "spaceconfigs"
	// legacyConfigPrefix is the pre-versioned layout: one document per Space.
	legacyConfigPrefix = "spacecfg"
)

// StateStore is a Store backed by durable state.
type StateStore struct {
	configs *state.Versions[Config]
	clock   Clock
}

var _ Store = (*StateStore)(nil)

// NewStateStore returns a Store persisting to st.
func NewStateStore(st state.Store, clock Clock) *StateStore {
	if clock == nil {
		clock = systemClock{}
	}
	return &StateStore{
		configs: state.NewVersions[Config](st, configPrefix).WithLegacy(legacyConfigPrefix),
		clock:   clock,
	}
}

// Get returns a Space's configuration.
func (s *StateStore) Get(ctx context.Context, spaceID string) (Config, error) {
	c, err := s.configs.Newest(ctx, spaceID)
	if err != nil {
		if state.IsNotFound(err) {
			return Config{}, ErrNotFound{SpaceID: spaceID}
		}
		return Config{}, fmt.Errorf("spacecfg: read configuration: %w", err)
	}
	return c, nil
}

// Put stores a new version of a Space's configuration, preserving CreatedAt.
func (s *StateStore) Put(ctx context.Context, c Config) (Config, error) {
	if err := validate(c); err != nil {
		return Config{}, err
	}

	now := s.clock.Now()
	c.UpdatedAt = now
	c.CreatedAt = now
	switch existing, err := s.configs.Newest(ctx, c.SpaceID); {
	case err == nil:
		c.CreatedAt = existing.CreatedAt
	case state.IsNotFound(err):
	default:
		return Config{}, fmt.Errorf("spacecfg: read configuration: %w", err)
	}

	if err := s.configs.Append(ctx, now, c, c.SpaceID); err != nil {
		return Config{}, fmt.Errorf("spacecfg: store configuration: %w", err)
	}
	return c, nil
}

// Delete removes a Space's configuration, every version of it. This is a user
// asking for the Space to stop being backed up, not a write racing a crash, so
// it is the one place a configuration is destroyed on purpose.
func (s *StateStore) Delete(ctx context.Context, spaceID string) error {
	removed, err := s.configs.DeleteAll(ctx, spaceID)
	if err != nil {
		return fmt.Errorf("spacecfg: delete configuration: %w", err)
	}
	if !removed {
		return ErrNotFound{SpaceID: spaceID}
	}
	return nil
}

// List returns every configuration, ordered by space id for determinism.
func (s *StateStore) List(ctx context.Context) ([]Config, error) {
	out, err := s.configs.Latest(ctx)
	if err != nil {
		return nil, fmt.Errorf("spacecfg: list configurations: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SpaceID < out[j].SpaceID })
	return out, nil
}
