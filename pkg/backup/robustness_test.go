package backup

// The two ways an unattended run stops being unattended: it never ends, or it
// ends and nobody hears about it.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/snapshot"
)

// blockingEngine never finishes on its own; only the run's deadline ends it.
type blockingEngine struct {
	*fakeEngine

	started chan struct{}
	once    sync.Once
}

func newBlockingEngine() *blockingEngine {
	return &blockingEngine{fakeEngine: &fakeEngine{}, started: make(chan struct{})}
}

func (e *blockingEngine) Snapshot(ctx context.Context, _ snapshot.Repo, _ snapshot.Source) (snapshot.Info, error) {
	e.once.Do(func() { close(e.started) })
	<-ctx.Done()
	return snapshot.Info{}, ctx.Err()
}

// A scheduled run has nobody watching it. A target that accepts a connection
// and then stops responding must not hold one of the scheduler's few run slots
// until the process is restarted.
func TestRunScheduled_IsBoundedByTheRunTimeout(t *testing.T) {
	engine := newBlockingEngine()
	h := newHarness(t, func(d *Deps) {
		d.Engine = engine
		d.RunTimeout = 50 * time.Millisecond
	})

	start := time.Now()
	_, err := h.runner.RunScheduled(context.Background(), testSpaceID)
	if err == nil {
		t.Fatal("a run that never finishes must fail")
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("the run was not bounded: %v", elapsed)
	}
	select {
	case <-engine.started:
	default:
		t.Fatal("the engine was never called; the test proves nothing")
	}

	running, err := h.jobs.ListRunning(context.Background())
	if err != nil {
		t.Fatalf("ListRunning: %v", err)
	}
	if len(running) != 0 {
		t.Fatalf("the timed-out run is still recorded as running: %+v", running)
	}

	history, err := h.jobs.ListRecent(context.Background(), testSpaceID, 1)
	if err != nil || len(history) != 1 {
		t.Fatalf("ListRecent = %+v (%v)", history, err)
	}
	if history[0].State != jobs.StateFailed {
		t.Fatalf("job state = %q, want failed", history[0].State)
	}
	// The snapshot error underneath describes whichever read was in flight when
	// the deadline passed, which tells a user nothing. The record must name the
	// actual cause.
	if history[0].Error != "the backup run timed out" {
		t.Fatalf("job error = %q", history[0].Error)
	}
}

// A manual run is bounded too, and always was: this pins that RunScheduled did
// not acquire its bound at the cost of the synchronous path losing one.
func TestRunBackup_IsNotBoundedByTheRunTimeout(t *testing.T) {
	engine := newBlockingEngine()
	h := newHarness(t, func(d *Deps) {
		d.Engine = engine
		d.RunTimeout = 50 * time.Millisecond
	})

	// A caller who waits owns the deadline; the runner must honour theirs.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if _, err := h.runner.RunBackup(ctx, testSpaceID); err == nil {
		t.Fatal("a cancelled caller must end the run")
	}
}

// deafStore accepts everything except the one write that matters.
type deafStore struct {
	*jobs.MemoryStore

	mu       sync.Mutex
	attempts int
}

func (d *deafStore) Finish(context.Context, string, jobs.Outcome) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attempts++
	return errors.New("state store unavailable")
}

func (d *deafStore) attemptCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.attempts
}

// The wedge, from the runner's side. If the outcome cannot be recorded, letting
// go of the run lock would leave a job stuck at "running" with nothing left to
// recover it from — every later scheduled run for that Space skipped, in
// silence, forever. Keeping the lock is what makes it recoverable.
func TestRunBackup_KeepsTheRunLockWhenTheOutcomeCannotBeRecorded(t *testing.T) {
	ctx := context.Background()

	var store *deafStore
	h := newHarness(t, func(d *Deps) {
		store = &deafStore{MemoryStore: d.Jobs.(*jobs.MemoryStore)}
		d.Jobs = store
	})

	if _, err := h.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}
	if got := store.attemptCount(); got != jobs.DefaultOutcomeAttempts {
		t.Fatalf("outcome write attempts = %d, want %d", got, jobs.DefaultOutcomeAttempts)
	}

	// The lock is still held, so recovery — not the next scheduled run — is
	// what closes this out.
	if _, err := h.runner.RunBackup(ctx, testSpaceID); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("second run = %v, want ErrRunInProgress", err)
	}
	if !strings.Contains(h.logs.String(), "keeping the run lock") {
		t.Fatalf("the kept lock was not explained in the log:\n%s", h.logs.String())
	}
}

// The ordinary case must still give the lock back, or one run per Space per
// process lifetime is all anyone gets.
func TestRunBackup_ReleasesTheRunLockOnceTheOutcomeIsRecorded(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	if _, err := h.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := h.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("second run: %v", err)
	}
}
