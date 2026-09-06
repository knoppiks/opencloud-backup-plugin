// Package notify tells people when their backups need attention.
//
// The failure this exists for is the silent one: a Space that quietly stopped
// backing up months ago is worse than one that never started, because the
// family believes they are covered. Every event is therefore *recorded* first
// and delivered second — a durable record is the part the status board and the
// tests rely on, delivery is best-effort on top.
//
// Audiences are split deliberately (decisions.md #15). A member of a Space
// hears about that Space: its run failed, its backups are stale. The operator
// hears about the machinery: a target is unreachable, credentials were
// rejected. Operator events carry **no space id, no user, no file counts** —
// the admin is a configuration actor, not a data actor, and must not learn
// which Spaces exist or how they are doing from a notification.
//
// Nothing here ever formats key material, credentials, paths or file names: the
// only free text an event carries is the sanitized message the runner already
// deemed safe to show a user.
package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"
)

// Kind classifies an event.
type Kind string

const (
	// KindRunFailed reports one failed run of a Space. Space-scoped.
	KindRunFailed Kind = "run_failed"
	// KindBackupStale reports that a Space has had no successful backup for
	// longer than its schedule allows. This is the important one: it is what
	// catches failures nobody noticed.
	KindBackupStale Kind = "backup_stale"
	// KindTargetUnavailable reports that a backup target could not be used.
	// Operator-scoped and deliberately anonymous.
	KindTargetUnavailable Kind = "target_unavailable"
)

// Audience decides who may see an event.
type Audience string

const (
	// AudienceSpaceMembers is for the members of one Space.
	AudienceSpaceMembers Audience = "space_members"
	// AudienceOperator is for whoever runs the service. Operator events must
	// never identify a Space or a user (decisions.md #15).
	AudienceOperator Audience = "operator"
)

// Event is one thing worth telling somebody about.
type Event struct {
	ID       string   `json:"id"`
	Kind     Kind     `json:"kind"`
	Audience Audience `json:"audience"`
	// SpaceID is set for space-scoped events only, and must be empty for
	// operator events.
	SpaceID string `json:"space_id,omitempty"`
	// Message is user-safe text. It never contains internal detail.
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// scopeOperator is the storage scope operator events live under. It is not a
// valid space id, so it cannot collide with one.
const scopeOperator = "operator"

// Scope returns the storage scope an event belongs to.
func (e Event) Scope() string {
	if e.Audience == AudienceOperator {
		return scopeOperator
	}
	return e.SpaceID
}

// Store persists events.
type Store interface {
	// Append records an event, filling in id and timestamp when absent.
	Append(ctx context.Context, e Event) (Event, error)
	// List returns the events of one scope, newest first. A scope is a space
	// id for member events; use ListOperator for operator events. A limit of
	// zero or less means "all".
	List(ctx context.Context, spaceID string, limit int) ([]Event, error)
	// ListOperator returns operator events, newest first.
	ListOperator(ctx context.Context, limit int) ([]Event, error)
	// PruneBefore deletes events created before cutoff.
	PruneBefore(ctx context.Context, cutoff time.Time) (int, error)
}

// Sink delivers an event somewhere a human will see it. Delivery failures are
// never fatal: the record is already durable.
type Sink interface {
	// Deliver sends one event. Name it in errors, never its recipients' data.
	Deliver(ctx context.Context, e Event) error
}

// Clock supplies the current time, injected so tests are deterministic.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// Notifier records events and fans them out to sinks.
type Notifier struct {
	store  Store
	sinks  []Sink
	clock  Clock
	logger *slog.Logger
}

// Options configures a Notifier.
type Options struct {
	// Sinks deliver events; may be empty, in which case events are recorded
	// only (and still visible through the API).
	Sinks []Sink
	// Clock is injected for deterministic tests.
	Clock Clock
	// Logger receives delivery diagnostics.
	Logger *slog.Logger
}

// New constructs a Notifier.
func New(store Store, opts Options) (*Notifier, error) {
	if store == nil {
		return nil, fmt.Errorf("notify: event store is required")
	}
	if opts.Clock == nil {
		opts.Clock = systemClock{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Notifier{store: store, sinks: opts.Sinks, clock: opts.Clock, logger: opts.Logger}, nil
}

// Notify records an event and delivers it. Recording is what must succeed;
// delivery failures are logged and swallowed, because a notification nobody
// could email is still a notification the status board must show.
func (n *Notifier) Notify(ctx context.Context, e Event) (Event, error) {
	if err := validate(e); err != nil {
		return Event{}, err
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = n.clock.Now()
	}

	recorded, err := n.store.Append(ctx, e)
	if err != nil {
		return Event{}, fmt.Errorf("notify: record event: %w", err)
	}

	for _, sink := range n.sinks {
		if err := sink.Deliver(ctx, recorded); err != nil {
			n.logger.Warn("could not deliver notification",
				"kind", string(recorded.Kind), "audience", string(recorded.Audience), "err", err)
		}
	}
	return recorded, nil
}

// SpaceEvent records an event for the members of one Space.
func (n *Notifier) SpaceEvent(ctx context.Context, kind Kind, spaceID, message string) (Event, error) {
	return n.Notify(ctx, Event{
		Kind:     kind,
		Audience: AudienceSpaceMembers,
		SpaceID:  spaceID,
		Message:  message,
	})
}

// OperatorEvent records an operational event. The signature has no space
// parameter on purpose: it is not possible to leak one through this path.
func (n *Notifier) OperatorEvent(ctx context.Context, kind Kind, message string) (Event, error) {
	return n.Notify(ctx, Event{
		Kind:     kind,
		Audience: AudienceOperator,
		Message:  message,
	})
}

// validate enforces the audience rules that decision #15 rests on.
func validate(e Event) error {
	if e.Kind == "" {
		return fmt.Errorf("notify: event kind is required")
	}
	switch e.Audience {
	case AudienceSpaceMembers:
		if e.SpaceID == "" {
			return fmt.Errorf("notify: a space-scoped event needs a space id")
		}
	case AudienceOperator:
		if e.SpaceID != "" {
			// An operator event that names a Space would hand the admin
			// visibility into users' backups (decisions.md #15).
			return fmt.Errorf("notify: an operator event must not name a space")
		}
	default:
		return fmt.Errorf("notify: unknown audience")
	}
	if e.Message == "" {
		return fmt.Errorf("notify: event message is required")
	}
	return nil
}

// newID returns a random, opaque event id.
func newID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("notify: generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
