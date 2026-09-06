package jobs

// The per-Space run lock, as a durable lease.
//
// Two different failure modes have to be covered, and they want different
// mechanisms:
//
//   - Two runs racing inside one process (a member pressing "back up now" while
//     the scheduler fires). Handled by a process-local mutex — exact, cheap, no
//     I/O.
//   - A process that died holding the lock. Handled by the durable lease: it
//     carries an expiry, it is renewed while the run is alive, and Recover
//     reaps expired ones and marks the abandoned run failed.
//
// The durable half deliberately does not attempt mutual exclusion between
// *concurrent* processes: the backend (OpenCloud's storage over CS3) has no
// compare-and-set, so a lease written after a read cannot be made atomic, and
// pretending otherwise would be the subtle bug rather than a fix for it. The
// deployment target is a single instance (decisions.md, phase-6 plan); running
// two instances against one state store is unsupported, and the lease's owner
// field is there to make that visible in diagnostics rather than to make it safe.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"opencloud-backup-plugin/pkg/state"
)

const (
	// leasePrefix roots lease documents.
	leasePrefix = "leases"
	// DefaultLeaseTTL is how long a lease stays valid without renewal. Short
	// enough that a crash frees the Space within minutes, long enough that a
	// brief state-store outage does not drop a live run's lock.
	DefaultLeaseTTL = 10 * time.Minute
	// leaseWriteTimeout bounds a renewal or release write, which run on their
	// own context because the run's context may already be cancelled.
	leaseWriteTimeout = 30 * time.Second
)

// MessageInterrupted is the sanitized error recorded on a run whose process
// disappeared. It says what the user needs to know and nothing else.
const MessageInterrupted = "the run was interrupted before it finished"

// lease is the durable half of a run lock.
type lease struct {
	SpaceID    string    `json:"space_id"`
	Owner      string    `json:"owner"`
	AcquiredAt time.Time `json:"acquired_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// LeaseLocker is a Locker whose locks survive — and expire after — a crash.
type LeaseLocker struct {
	docs   *state.Documents[lease]
	clock  Clock
	ttl    time.Duration
	owner  string
	logger *slog.Logger
	after  func(time.Duration) <-chan time.Time

	mu    sync.Mutex
	local map[string]chan struct{}
}

var _ Locker = (*LeaseLocker)(nil)

// LeaseOptions configures a LeaseLocker. The zero value is usable.
type LeaseOptions struct {
	// TTL is the lease lifetime; zero uses DefaultLeaseTTL.
	TTL time.Duration
	// Clock is injected for deterministic tests.
	Clock Clock
	// Logger receives lease diagnostics. It is never given key material.
	Logger *slog.Logger
	// Owner identifies this process in lease records; zero generates one.
	Owner string
	// After schedules lease renewals. Tests inject a channel they control.
	After func(time.Duration) <-chan time.Time
}

// NewLeaseLocker returns a Locker persisting leases to st.
func NewLeaseLocker(st state.Store, opts LeaseOptions) (*LeaseLocker, error) {
	if st == nil {
		return nil, fmt.Errorf("jobs: state store is required")
	}
	if opts.TTL <= 0 {
		opts.TTL = DefaultLeaseTTL
	}
	if opts.Clock == nil {
		opts.Clock = systemClock{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.After == nil {
		opts.After = time.After
	}
	if opts.Owner == "" {
		owner, err := newOwnerID()
		if err != nil {
			return nil, err
		}
		opts.Owner = owner
	}
	return &LeaseLocker{
		docs:   state.NewDocuments[lease](st, leasePrefix),
		clock:  opts.Clock,
		ttl:    opts.TTL,
		owner:  opts.Owner,
		logger: opts.Logger,
		after:  opts.After,
		local:  make(map[string]chan struct{}),
	}, nil
}

// Acquire takes the Space's run lock.
func (l *LeaseLocker) Acquire(ctx context.Context, spaceID string) (func(), error) {
	if spaceID == "" {
		return nil, fmt.Errorf("jobs: space id required")
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, held := l.local[spaceID]; held {
		return nil, ErrLocked
	}

	now := l.clock.Now()
	switch current, err := l.docs.Get(ctx, spaceID); {
	case err == nil:
		if now.Before(current.ExpiresAt) {
			return nil, ErrLocked
		}
		// Expired: the holder is gone. Recover cleans up the job record; taking
		// the lock here is what stops one crash from wedging a Space forever.
		l.logger.Warn("taking over an expired run lease", "space", spaceID)
	case state.IsNotFound(err):
	default:
		return nil, fmt.Errorf("jobs: read run lease: %w", err)
	}

	if err := l.write(ctx, spaceID, now); err != nil {
		return nil, err
	}

	stop := make(chan struct{})
	l.local[spaceID] = stop
	go l.renewUntil(spaceID, stop)

	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			l.release(spaceID)
		})
	}, nil
}

// write stores a lease valid from now.
func (l *LeaseLocker) write(ctx context.Context, spaceID string, now time.Time) error {
	rec := lease{
		SpaceID:    spaceID,
		Owner:      l.owner,
		AcquiredAt: now,
		ExpiresAt:  now.Add(l.ttl),
	}
	if err := l.docs.Put(ctx, rec, spaceID); err != nil {
		return fmt.Errorf("jobs: write run lease: %w", err)
	}
	return nil
}

// renewUntil keeps a held lease alive until the run releases it. A run may
// legitimately take hours; the lease stays short so a crash is noticed quickly.
func (l *LeaseLocker) renewUntil(spaceID string, stop <-chan struct{}) {
	interval := l.ttl / 3
	if interval <= 0 {
		interval = l.ttl
	}
	for {
		select {
		case <-stop:
			return
		case <-l.after(interval):
			ctx, cancel := context.WithTimeout(context.Background(), leaseWriteTimeout)
			err := l.write(ctx, spaceID, l.clock.Now())
			cancel()
			if err != nil {
				// Best effort: if renewal keeps failing the lease expires and
				// the Space is recovered, which is the safe direction.
				l.logger.Warn("could not renew run lease", "space", spaceID, "err", err)
			}
		}
	}
}

// release drops the lease and the process-local hold.
func (l *LeaseLocker) release(spaceID string) {
	l.mu.Lock()
	delete(l.local, spaceID)
	l.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), leaseWriteTimeout)
	defer cancel()
	if err := l.docs.Delete(ctx, spaceID); err != nil && !state.IsNotFound(err) {
		l.logger.Warn("could not release run lease", "space", spaceID, "err", err)
	}
}

// Recover closes out runs abandoned by a process that died holding a lease: the
// lease is dropped and the Space's unfinished jobs are marked failed, so the
// history shows what happened instead of a run that is "running" forever.
//
// It is safe to call repeatedly — the scheduler calls it on every tick — and it
// leaves live leases (including this process's own) alone.
func (l *LeaseLocker) Recover(ctx context.Context, store Store) (int, error) {
	keys, err := l.docs.Keys(ctx)
	if err != nil {
		return 0, fmt.Errorf("jobs: list run leases: %w", err)
	}

	now := l.clock.Now()
	recovered := 0
	for _, key := range keys {
		rec, err := l.docs.GetKey(ctx, key)
		if err != nil {
			continue
		}
		if now.Before(rec.ExpiresAt) || l.holdsLocally(rec.SpaceID) {
			continue
		}

		failed, err := l.failAbandoned(ctx, store, rec.SpaceID)
		if err != nil {
			return recovered, err
		}
		if err := l.docs.DeleteKey(ctx, key); err != nil && !state.IsNotFound(err) {
			return recovered, fmt.Errorf("jobs: drop expired run lease: %w", err)
		}
		l.logger.Warn("recovered an abandoned run", "space", rec.SpaceID, "jobs", failed)
		recovered += failed
	}
	return recovered, nil
}

// failAbandoned marks a Space's unfinished jobs as failed.
func (l *LeaseLocker) failAbandoned(ctx context.Context, store Store, spaceID string) (int, error) {
	if store == nil {
		return 0, nil
	}
	list, err := store.List(ctx, spaceID)
	if err != nil {
		return 0, fmt.Errorf("jobs: list abandoned runs: %w", err)
	}

	failed := 0
	for _, j := range list {
		if j.State.Terminal() {
			continue
		}
		if err := store.Finish(ctx, j.ID, Outcome{State: StateFailed, Error: MessageInterrupted}); err != nil {
			return failed, fmt.Errorf("jobs: close abandoned run: %w", err)
		}
		failed++
	}
	return failed, nil
}

func (l *LeaseLocker) holdsLocally(spaceID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, held := l.local[spaceID]
	return held
}

// newOwnerID is used when no owner is configured; kept separate from job ids so
// the intent reads clearly at the call site.
func newOwnerID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("jobs: generate owner id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
