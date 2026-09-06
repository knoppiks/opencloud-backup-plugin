package notify

// The durable event store, on top of pkg/state.
//
// Layout mirrors pkg/jobs: "notifications/<scope>/<created-unix-nanos>-<id>",
// where scope is a space id or the operator scope. Chronological key order
// again means "the last five notifications" reads five documents, and the
// scope prefix is what keeps a member's events and the operator's events
// physically separate rather than separated by a filter somebody might forget.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"opencloud-backup-plugin/pkg/state"
)

// eventPrefix roots event documents.
const eventPrefix = "notifications"

// keyTimeWidth pads the nanosecond timestamp so keys sort lexically.
const keyTimeWidth = 19

// StateStore is a Store backed by durable state.
type StateStore struct {
	docs  *state.Documents[Event]
	clock Clock
}

var _ Store = (*StateStore)(nil)

// NewStateStore returns a Store persisting to st.
func NewStateStore(st state.Store, clock Clock) *StateStore {
	if clock == nil {
		clock = systemClock{}
	}
	return &StateStore{docs: state.NewDocuments[Event](st, eventPrefix), clock: clock}
}

// Append records an event.
func (s *StateStore) Append(ctx context.Context, e Event) (Event, error) {
	if err := validate(e); err != nil {
		return Event{}, err
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = s.clock.Now()
	}
	if e.ID == "" {
		id, err := newID()
		if err != nil {
			return Event{}, err
		}
		e.ID = id
	}

	key := s.docs.Key(e.Scope(), documentName(e.CreatedAt, e.ID))
	if err := s.docs.PutKey(ctx, key, e); err != nil {
		return Event{}, fmt.Errorf("notify: store event: %w", err)
	}
	return e, nil
}

// List returns a Space's events, newest first.
func (s *StateStore) List(ctx context.Context, spaceID string, limit int) ([]Event, error) {
	if spaceID == "" {
		return nil, fmt.Errorf("notify: space id required")
	}
	return s.listScope(ctx, spaceID, limit)
}

// ListOperator returns operator events, newest first.
func (s *StateStore) ListOperator(ctx context.Context, limit int) ([]Event, error) {
	return s.listScope(ctx, scopeOperator, limit)
}

func (s *StateStore) listScope(ctx context.Context, scope string, limit int) ([]Event, error) {
	keys, err := s.docs.Keys(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("notify: list events: %w", err)
	}
	if limit > 0 && len(keys) > limit {
		keys = keys[len(keys)-limit:]
	}

	out := make([]Event, 0, len(keys))
	for i := len(keys) - 1; i >= 0; i-- {
		e, err := s.docs.GetKey(ctx, keys[i])
		if err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// PruneBefore deletes events created before cutoff. Candidates are chosen from
// key names, so no document is read to decide.
func (s *StateStore) PruneBefore(ctx context.Context, cutoff time.Time) (int, error) {
	keys, err := s.docs.Keys(ctx)
	if err != nil {
		return 0, fmt.Errorf("notify: list events: %w", err)
	}

	removed := 0
	for _, key := range keys {
		created, ok := createdFromKey(key)
		if !ok || !created.Before(cutoff) {
			continue
		}
		if err := s.docs.DeleteKey(ctx, key); err != nil && !state.IsNotFound(err) {
			return removed, fmt.Errorf("notify: prune events: %w", err)
		}
		removed++
	}
	return removed, nil
}

// documentName gives events the same chronological key shape jobs use.
func documentName(created time.Time, id string) string {
	return fmt.Sprintf("%0*d-%s", keyTimeWidth, created.UTC().UnixNano(), id)
}

// createdFromKey extracts an event's creation time from a document key.
func createdFromKey(key string) (time.Time, bool) {
	name := key[strings.LastIndex(key, "/")+1:]
	sep := strings.Index(name, "-")
	if sep < 0 {
		return time.Time{}, false
	}
	nanos, err := strconv.ParseInt(name[:sep], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, nanos).UTC(), true
}
