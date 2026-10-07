package jobs

// Work that outlives the request that started it, but not the process.
//
// A manual backup or restore is accepted by an HTTP handler and then runs for
// up to hours. It used to run on the request's context with cancellation
// stripped, in a goroutine nobody tracked: SIGTERM did not reach it, shutdown
// did not wait for it, and a panic inside it took the whole process — every
// other run and the scheduler — down with it (review-2026-10.md F7).
// Background is the owner those goroutines were missing.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
)

// OutcomeWorstCase is how long RecordOutcome can take with the production
// settings: every attempt timing out, with the backoff between them. Shutdown
// derives its drain budget from it, so the two cannot drift apart.
const OutcomeWorstCase = DefaultOutcomeAttempts*DefaultOutcomeTimeout +
	(DefaultOutcomeAttempts-1)*DefaultOutcomeBackoff

// ErrShuttingDown is returned for work offered after shutdown began.
var ErrShuttingDown = errors.New("jobs: the service is shutting down")

// Background runs tracked goroutines on a context the service cancels at
// shutdown. The zero value is not usable; use NewBackground.
type Background struct {
	ctx    context.Context
	cancel context.CancelFunc
	logger *slog.Logger

	mu      sync.Mutex
	stopped bool
	wg      sync.WaitGroup
}

// NewBackground returns a Background whose work runs until Stop.
func NewBackground(logger *slog.Logger) *Background {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Background{ctx: ctx, cancel: cancel, logger: logger}
}

// Go runs fn in a tracked goroutine. fn's context is cancelled by Stop. A
// panic in fn is logged and contained; callers that need to turn a panic into
// a recorded failure do so themselves (see Recover), because only they know
// what failed.
//
// After Stop, Go refuses with ErrShuttingDown and does not run fn.
func (b *Background) Go(name string, fn func(ctx context.Context)) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return ErrShuttingDown
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer func() {
			if v := recover(); v != nil {
				b.logger.Error("background task panicked",
					"task", name, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			}
		}()
		fn(b.ctx)
	}()
	return nil
}

// Stop cancels every running task and refuses new ones. It does not wait.
func (b *Background) Stop() {
	b.mu.Lock()
	b.stopped = true
	b.mu.Unlock()
	b.cancel()
}

// Wait blocks until every task started by Go has returned.
func (b *Background) Wait() { b.wg.Wait() }

// PanicError is a panic converted into an error, so a run that panicked fails
// through the same path as one that returned an error.
type PanicError struct {
	// Value is what was passed to panic.
	Value any
	// Stack is the goroutine's stack at the panic, for the operator's log.
	Stack []byte
}

func (e *PanicError) Error() string { return fmt.Sprintf("panic: %v", e.Value) }

// Recover runs fn and converts a panic in it into a *PanicError. A panic in
// kopia or a CS3 call then fails the one run it happened in, instead of the
// process and every other run in it.
func Recover[T any](fn func() (T, error)) (result T, err error) {
	defer func() {
		if v := recover(); v != nil {
			var zero T
			result, err = zero, &PanicError{Value: v, Stack: debug.Stack()}
		}
	}()
	return fn()
}

// PanicStack returns the stack recorded for a panic anywhere in err's chain,
// or "" when err is not one. It exists so a log line can carry the stack
// without the error message doing so.
func PanicStack(err error) string {
	var p *PanicError
	if errors.As(err, &p) {
		return string(p.Stack)
	}
	return ""
}
