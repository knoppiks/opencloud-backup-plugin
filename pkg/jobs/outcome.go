package jobs

// Recording a run's terminal outcome, durably.
//
// This is one write, and it is the write everything else depends on. A run that
// finishes but cannot say so leaves a record at "running" that no later tick
// re-examines: the Space then looks permanently busy, every scheduled run is
// skipped, and the only symptom is that backups stopped. It is the cheapest
// possible failure with the most expensive consequence in this codebase, which
// is why it does not share the run's context and does not give up on the first
// error.

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	// DefaultOutcomeAttempts is how many times a terminal outcome is written
	// before the caller is told it did not stick.
	DefaultOutcomeAttempts = 3
	// DefaultOutcomeTimeout bounds one attempt. Kept short on purpose: all the
	// attempts together happen inside the shutdown drain window, so a service
	// stopping must not be held up by a state store that is already gone.
	DefaultOutcomeTimeout = 10 * time.Second
	// DefaultOutcomeBackoff is the pause between attempts. Long enough for a
	// state store that is restarting, short enough that a caller still holding
	// a run lock is not held up for minutes.
	DefaultOutcomeBackoff = 2 * time.Second
)

// RecordOptions tunes RecordOutcome. The zero value is the production setting;
// tests shorten the backoff and substitute Sleep.
type RecordOptions struct {
	// Attempts is the number of writes tried; zero uses the default.
	Attempts int
	// Timeout bounds one attempt; zero uses the default.
	Timeout time.Duration
	// Backoff is the pause between attempts; zero uses the default.
	Backoff time.Duration
	// Sleep waits, or returns early when the context ends; nil sleeps for real.
	Sleep func(ctx context.Context, d time.Duration)
}

// RecordOutcome stores a run's terminal outcome, retrying before it gives up.
//
// Two things distinguish it from calling Store.Finish directly, and both exist
// because a lost outcome write is what wedges a Space:
//
//   - It writes on a context detached from the run's. A run cancelled by
//     shutdown, or stopped by its own deadline, must still be able to record
//     why; with the run's own context the write fails for the very reason the
//     run did.
//   - It retries. A state store that blinks then costs a pause, not a job stuck
//     at "running" forever.
//
// A non-nil error is the caller's signal to keep the Space's run lock, so the
// lease expires and Recover closes the run out (see lease.go). Errors that
// retrying cannot fix — an unknown job, a non-terminal outcome — are returned
// immediately.
func RecordOutcome(ctx context.Context, store Store, id string, out Outcome, opts RecordOptions) error {
	if store == nil {
		return fmt.Errorf("jobs: job store is required")
	}
	attempts, timeout, backoff, sleep := opts.resolve()

	// Detached deliberately: see the doc comment. The deadline is per attempt.
	base := context.WithoutCancel(ctx)

	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			sleep(base, backoff)
		}

		attemptCtx, cancel := context.WithTimeout(base, timeout)
		err = store.Finish(attemptCtx, id, out)
		cancel()

		switch {
		case err == nil:
			return nil
		case permanent(err):
			return err
		}
	}
	return fmt.Errorf("jobs: record outcome after %d attempts: %w", attempts, err)
}

// permanent reports whether retrying an outcome write is pointless.
func permanent(err error) bool {
	var notFound ErrNotFound
	return errors.Is(err, ErrNotTerminal) || errors.As(err, &notFound)
}

// resolve fills in the production defaults.
func (o RecordOptions) resolve() (attempts int, timeout, backoff time.Duration, sleep func(context.Context, time.Duration)) {
	attempts, timeout, backoff, sleep = o.Attempts, o.Timeout, o.Backoff, o.Sleep
	if attempts <= 0 {
		attempts = DefaultOutcomeAttempts
	}
	if timeout <= 0 {
		timeout = DefaultOutcomeTimeout
	}
	if backoff < 0 {
		backoff = 0
	} else if backoff == 0 {
		backoff = DefaultOutcomeBackoff
	}
	if sleep == nil {
		sleep = sleepFor
	}
	return attempts, timeout, backoff, sleep
}

// sleepFor waits for d, or until ctx ends.
func sleepFor(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
