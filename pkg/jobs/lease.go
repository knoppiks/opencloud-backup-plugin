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
// The durable half attempts mutual exclusion between *concurrent* processes but
// cannot guarantee it: Acquire refuses a Space whose lease is live in the store,
// which is a check and not a lock. The backend (OpenCloud's storage over CS3)
// has no compare-and-set, so a lease written after a read cannot be made atomic,
// and treating that check as a lock would be the subtle bug rather than a fix
// for it. The
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
	// DefaultOrphanSweep is how often Recover looks for runs recorded as
	// running that no lease corresponds to. It is slow because the sweep reads
	// job documents while the lease pass only lists leases, and because the
	// thing it catches — an outcome write that was lost after the lock was
	// dropped — is rare by construction.
	DefaultOrphanSweep = time.Hour
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
	docs        *state.Documents[lease]
	clock       Clock
	ttl         time.Duration
	orphanSweep time.Duration
	owner       string
	logger      *slog.Logger
	after       func(time.Duration) <-chan time.Time

	mu         sync.Mutex
	local      map[string]chan struct{}
	lastOrphan time.Time
}

var _ Locker = (*LeaseLocker)(nil)

// LeaseOptions configures a LeaseLocker. The zero value is usable.
type LeaseOptions struct {
	// TTL is the lease lifetime; zero uses DefaultLeaseTTL.
	TTL time.Duration
	// OrphanSweep is how often Recover looks for running jobs with no lease;
	// zero uses DefaultOrphanSweep.
	OrphanSweep time.Duration
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
	if opts.OrphanSweep <= 0 {
		opts.OrphanSweep = DefaultOrphanSweep
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
		docs:        state.NewDocuments[lease](st, leasePrefix),
		clock:       opts.Clock,
		ttl:         opts.TTL,
		orphanSweep: opts.OrphanSweep,
		owner:       opts.Owner,
		logger:      opts.Logger,
		after:       opts.After,
		local:       make(map[string]chan struct{}),
		// The first orphan sweep is one interval away, not at startup: the
		// lease pass already covers everything a crashed process left behind,
		// and reading the whole run history is not what a service should do
		// before it accepts its first request.
		lastOrphan: opts.Clock.Now(),
	}, nil
}

// Acquire takes the Space's run lock.
//
// The process-local reservation is taken first and the mutex is dropped before
// any I/O: the durable read and write below go to OpenCloud, and holding the
// lock that serialises every Space across a network round-trip would make one
// slow state store stall every other Space's run. The reservation is undone on
// every failure path, so a refused acquire leaves nothing behind.
func (l *LeaseLocker) Acquire(ctx context.Context, spaceID string) (func(), error) {
	if spaceID == "" {
		return nil, fmt.Errorf("jobs: space id required")
	}

	stop, ok := l.reserve(spaceID)
	if !ok {
		return nil, ErrLocked
	}

	now := l.clock.Now()
	switch current, err := l.docs.Get(ctx, spaceID); {
	case err == nil:
		if now.Before(current.ExpiresAt) {
			l.undoReserve(spaceID, stop)
			return nil, ErrLocked
		}
		// Expired: the holder is gone. Recover cleans up the job record; taking
		// the lock here is what stops one crash from wedging a Space forever.
		l.logger.Warn("taking over an expired run lease", "space", spaceID)
	case state.IsNotFound(err):
	default:
		l.undoReserve(spaceID, stop)
		return nil, fmt.Errorf("jobs: read run lease: %w", err)
	}

	if err := l.write(ctx, spaceID, now); err != nil {
		l.undoReserve(spaceID, stop)
		return nil, err
	}

	go l.renewUntil(spaceID, stop)

	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			l.release(spaceID)
		})
	}, nil
}

// reserve claims the Space in this process, reporting whether it was free.
func (l *LeaseLocker) reserve(spaceID string) (chan struct{}, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, held := l.local[spaceID]; held {
		return nil, false
	}
	stop := make(chan struct{})
	l.local[spaceID] = stop
	return stop, true
}

// undoReserve gives the local claim back, and only if it is still ours.
func (l *LeaseLocker) undoReserve(spaceID string, stop chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if held, ok := l.local[spaceID]; ok && held == stop {
		delete(l.local, spaceID)
	}
}

// write stores a lease valid from now.
func (l *LeaseLocker) write(ctx context.Context, spaceID string, now time.Time) error {
	rec := lease{
		SpaceID:    spaceID,
		Owner:      l.owner,
		AcquiredAt: now,
		ExpiresAt:  now.Add(l.ttl),
	}
	// A lease is re-derivable state: it expires on its own, and losing one
	// costs at worst a run that has to wait for the TTL. Replacing it in place
	// is what renewal means.
	if err := l.docs.Replace(ctx, rec, spaceID); err != nil {
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

// Recover closes out runs nothing is executing any more, in two passes:
//
//   - Expired leases: a process died holding the lock. The lease is dropped and
//     the Space's unfinished jobs are marked failed, so the history shows what
//     happened instead of a run that is "running" forever.
//   - Orphaned records: a run that ended but could not record its outcome, and
//     whose lock is already gone. Nothing else would ever revisit these, and a
//     Space carrying one looks permanently busy. This pass reads job documents,
//     so it runs on its own slow cadence (LeaseOptions.OrphanSweep) rather than
//     on every tick.
//
// It is safe to call repeatedly — the scheduler calls it on every tick — and it
// leaves live leases (including this process's own) alone.
func (l *LeaseLocker) Recover(ctx context.Context, store Store) (int, error) {
	recovered, err := l.recoverExpired(ctx, store)
	if err != nil {
		return recovered, err
	}
	orphans, err := l.recoverOrphans(ctx, store)
	return recovered + orphans, err
}

// recoverExpired reaps leases whose holder is gone.
func (l *LeaseLocker) recoverExpired(ctx context.Context, store Store) (int, error) {
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

// recoverOrphans closes out jobs recorded as running that no lock corresponds
// to.
//
// This is the failure the expired-lease pass cannot see. A run that ends
// releases its lock and then records its outcome; if that record is lost the
// lock is already gone, so there is no lease to expire and no trace left for
// the pass above. The Space is then wedged: every scheduler tick sees a run in
// progress that no longer exists. Reading the outcome the other way round — the
// jobs first, the leases second — is what closes it.
//
// Absence of a lease is only evidence once: a job whose run is genuinely under
// way is either held in this process or covered by a live lease, and a run
// takes its lock before it creates its job record, so there is no window in
// which a legitimate run looks orphaned.
func (l *LeaseLocker) recoverOrphans(ctx context.Context, store Store) (int, error) {
	if store == nil || !l.orphanSweepDue() {
		return 0, nil
	}

	running, err := store.ListRunning(ctx)
	if err != nil {
		return 0, fmt.Errorf("jobs: list running runs: %w", err)
	}

	recovered := 0
	for _, j := range running {
		if l.holdsLocally(j.SpaceID) {
			continue
		}
		live, err := l.leaseLive(ctx, j.SpaceID)
		if err != nil {
			return recovered, err
		}
		if live {
			continue
		}
		if err := store.Finish(ctx, j.ID, Outcome{State: StateFailed, Error: MessageInterrupted}); err != nil {
			return recovered, fmt.Errorf("jobs: close orphaned run: %w", err)
		}
		l.logger.Warn("closed out a run with no lock behind it", "space", j.SpaceID, "job", j.ID)
		recovered++
	}
	return recovered, nil
}

// orphanSweepDue reports whether the slow pass is due, and claims the slot.
func (l *LeaseLocker) orphanSweepDue() bool {
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastOrphan) < l.orphanSweep {
		return false
	}
	l.lastOrphan = now
	return true
}

// Busy reports whether a run holds the Space's lock, here or in the durable
// lease. It answers the scheduler's "is something already running" question
// without reading run history, and it counts every kind of run — a restore
// holds the same lock a backup does.
//
// An expired lease is not busy: its holder is gone, and Recover will close the
// run out.
func (l *LeaseLocker) Busy(ctx context.Context, spaceID string) (bool, error) {
	if l.holdsLocally(spaceID) {
		return true, nil
	}
	return l.leaseLive(ctx, spaceID)
}

// leaseLive reports whether a Space's durable lease exists and has not expired.
func (l *LeaseLocker) leaseLive(ctx context.Context, spaceID string) (bool, error) {
	rec, err := l.docs.Get(ctx, spaceID)
	switch {
	case state.IsNotFound(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("jobs: read run lease: %w", err)
	}
	return l.clock.Now().Before(rec.ExpiresAt), nil
}

// ActiveLeases counts the run leases that have not expired at now.
//
// It exists for the operations that must not run while a backup does — rotating
// a wrapping key, above all, which would otherwise re-wrap an envelope a live
// run is about to open. It is a package-level function rather than a method
// because the caller (an operator CLI) holds no locker and must claim nothing:
// asking is the whole point.
//
// This is a check, not a lock. There is no compare-and-set here (see the note at
// the top of this file), so a run can start the instant after it answers. It
// catches the operator who forgot to stop the service; it does not make
// concurrent rotation safe.
func ActiveLeases(ctx context.Context, st state.Store, now time.Time) (int, error) {
	docs := state.NewDocuments[lease](st, leasePrefix)
	keys, err := docs.Keys(ctx)
	if err != nil {
		return 0, fmt.Errorf("jobs: list run leases: %w", err)
	}

	active := 0
	for _, key := range keys {
		rec, err := docs.GetKey(ctx, key)
		if err != nil {
			// An unreadable lease is not evidence of an idle service. Count it:
			// the safe direction here is to refuse.
			active++
			continue
		}
		if now.Before(rec.ExpiresAt) {
			active++
		}
	}
	return active, nil
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
