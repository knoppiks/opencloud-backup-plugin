// Package instance makes the single-instance constraint visible.
//
// decisions.md #16 permits exactly one service instance against one state Space:
// the state backend (OpenCloud storage over CS3) has no compare-and-set, so the
// run lease cannot be made mutually exclusive between processes. Until now that
// was a sentence in a document, and the shipped deployment violated it on every
// rolling update.
//
// A guard writes a short-lived record announcing itself and refuses to start
// while another instance is renewing its record; a record that stops being
// renewed is waited out instead. **This is not a lock.** Two
// processes starting at the same moment can both read "nobody there" and both
// write; the same missing primitive that stops the lease from being a lock stops
// this from being one. What it does catch is the common case — a second instance
// starting while the first is running, which is exactly what a rolling update
// does — and it turns "unsupported" into "refused".
package instance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/knoppiks/opencloud-backup-plugin/internal/state"
)

const (
	// prefix roots instance records.
	prefix = "instances"
	// DefaultTTL is how long a record stays live without renewal. Short, because
	// its only job is to answer "is somebody else running right now"; a stale
	// record delays a legitimate restart by at most this long.
	DefaultTTL = 2 * time.Minute
	// writeTimeout bounds renewal and release writes, which run on their own
	// context because shutdown has usually cancelled everything else.
	writeTimeout = 30 * time.Second
)

// ErrAnotherInstance reports that another instance is alive: its record was
// renewed while this one watched it. It is a startup error: two instances
// against one state Space can mark each other's runs failed and dispatch the
// same Space twice.
var ErrAnotherInstance = errors.New(
	"instance: another instance of this service is running against this state space; " +
		"only one is supported (decisions.md #16) — deploy with strategy Recreate")

// record is one instance's announcement of itself.
type record struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"started_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Clock is the time source, injected so tests are deterministic.
type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// SystemClock returns the real clock.
func SystemClock() Clock { return systemClock{} }

// Options configures a Guard. The zero value is usable.
type Options struct {
	// TTL is the record lifetime; zero uses DefaultTTL.
	TTL time.Duration
	// ID identifies this process; zero generates one.
	ID string
	// Clock is injected for deterministic tests.
	Clock Clock
	// Logger receives diagnostics. It never sees key material.
	Logger *slog.Logger
	// After schedules renewals and the re-reads of a record Claim waits on.
	// Tests inject a channel they control.
	After func(time.Duration) <-chan time.Time
}

// Guard claims and holds this process's instance record.
type Guard struct {
	docs   *state.Documents[record]
	ttl    time.Duration
	id     string
	clock  Clock
	logger *slog.Logger
	after  func(time.Duration) <-chan time.Time
}

// New builds a Guard over the service's state store.
func New(st state.Store, opts Options) (*Guard, error) {
	if st == nil {
		return nil, fmt.Errorf("instance: state store is required")
	}
	if opts.TTL <= 0 {
		opts.TTL = DefaultTTL
	}
	if opts.Clock == nil {
		opts.Clock = SystemClock()
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.After == nil {
		opts.After = time.After
	}
	if opts.ID == "" {
		id, err := newID()
		if err != nil {
			return nil, err
		}
		opts.ID = id
	}
	return &Guard{
		docs:   state.NewDocuments[record](st, prefix),
		ttl:    opts.TTL,
		id:     opts.ID,
		clock:  opts.Clock,
		logger: opts.Logger,
		after:  opts.After,
	}, nil
}

// ID returns this instance's identifier, for diagnostics.
func (g *Guard) ID() string { return g.id }

// Claim announces this instance and returns the function that stands it down.
// It returns ErrAnotherInstance when someone else's record is renewed while
// Claim watches it, and waits (at most the TTL, or until ctx ends) for one
// that is not.
//
// An unreadable record counts as live: a state store that cannot be understood
// is not evidence that nobody is there, and refusing to start is the recoverable
// direction.
func (g *Guard) Claim(ctx context.Context) (func(), error) {
	if err := g.checkAlone(ctx); err != nil {
		return nil, err
	}
	if err := g.write(ctx); err != nil {
		return nil, err
	}

	// Renewing and withdrawing the record outlive ctx, which is startup's:
	// they keep its values and drop its cancellation.
	detached := context.WithoutCancel(ctx)
	stop := make(chan struct{})
	go g.renewUntil(detached, stop)

	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			g.release(detached)
		})
	}, nil
}

// checkAlone refuses when another instance is alive, and clears the records of
// instances that are demonstrably gone.
func (g *Guard) checkAlone(ctx context.Context) error {
	keys, err := g.docs.Keys(ctx)
	if err != nil {
		return fmt.Errorf("instance: list instance records: %w", err)
	}

	for _, key := range keys {
		rec, err := g.docs.GetKey(ctx, key)
		if err != nil {
			return fmt.Errorf("%w (an instance record could not be read)", ErrAnotherInstance)
		}
		if rec.ID == g.id {
			continue
		}
		if g.clock.Now().Before(rec.ExpiresAt) {
			if err := g.awaitExpiry(ctx, key, rec); err != nil {
				return err
			}
		}
		// Expired: that process is gone, or is so far behind on renewals that
		// it has already lost its run leases.
		if err := g.docs.DeleteKey(ctx, key); err != nil && !state.IsNotFound(err) {
			g.logger.Warn("could not clear a stale instance record", "err", err)
		}
	}
	return nil
}

// awaitExpiry watches a live record until it expires or is withdrawn, and
// refuses as soon as it is renewed.
//
// A record outlives its process by up to the TTL when that process did not
// stand down: a crash, or a host that went down under it. The replacement
// that starts after a reboot then finds it live. Refusing would make it
// restart until the TTL ran out; waiting costs at most the same TTL. What
// tells a dead instance from a live one is renewal, which a live one does
// every TTL/3, so the record is watched rather than merely waited out
// (decisions.md #16, amended for #152).
func (g *Guard) awaitExpiry(ctx context.Context, key string, seen record) error {
	g.logger.Info("another instance's record is live; waiting to see whether it is renewed",
		"instance", seen.ID, "expires_at", seen.ExpiresAt)
	for {
		remaining := seen.ExpiresAt.Sub(g.clock.Now())
		if remaining <= 0 {
			g.logger.Info("the other instance's record expired unrenewed; taking over", "instance", seen.ID)
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("instance: waiting for another instance's record to expire: %w", ctx.Err())
		case <-g.after(min(g.watchInterval(), remaining)):
		}

		rec, err := g.docs.GetKey(ctx, key)
		switch {
		case state.IsNotFound(err):
			return nil
		case err != nil:
			return fmt.Errorf("%w (an instance record could not be read)", ErrAnotherInstance)
		case !rec.ExpiresAt.Equal(seen.ExpiresAt):
			return ErrAnotherInstance
		}
	}
}

// watchInterval is how often a live record is re-read while waiting for it to
// expire: often enough to notice a renewal well before the next one is due.
func (g *Guard) watchInterval() time.Duration { return g.ttl / 12 }

// write stores this instance's record, valid from now.
func (g *Guard) write(ctx context.Context) error {
	now := g.clock.Now()
	rec := record{ID: g.id, StartedAt: now, ExpiresAt: now.Add(g.ttl)}
	// Re-derivable state: it expires on its own and renewal *is* replacement.
	if err := g.docs.Replace(ctx, rec, g.id); err != nil {
		return fmt.Errorf("instance: write instance record: %w", err)
	}
	return nil
}

// renewUntil keeps the record live for as long as this process is.
func (g *Guard) renewUntil(ctx context.Context, stop <-chan struct{}) {
	interval := g.ttl / 3
	if interval <= 0 {
		interval = g.ttl
	}
	for {
		select {
		case <-stop:
			return
		case <-g.after(interval):
			writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := g.write(writeCtx)
			cancel()
			if err != nil {
				// Best effort: a record that stops being renewed expires, and
				// the next instance is allowed to start. That is the safe
				// direction — the alternative is refusing forever.
				g.logger.Warn("could not renew this instance's record", "err", err)
			}
		}
	}
}

// release drops this instance's record so a replacement can start immediately
// instead of waiting out the TTL.
func (g *Guard) release(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := g.docs.Delete(ctx, g.id); err != nil && !state.IsNotFound(err) {
		g.logger.Warn("could not clear this instance's record", "err", err)
	}
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("instance: generate instance id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
