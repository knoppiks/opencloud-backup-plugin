// Package retry waits for a dependency that is not there yet.
//
// It exists for startup: the service and OpenCloud start together after a
// reboot, and the service used to exit when it came first (#152). Retrying is
// only for errors the caller calls transient; anything else returns at once,
// so a misconfiguration still fails fast instead of being waited on.
package retry

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Defaults for Options' zero values.
const (
	DefaultInitial  = time.Second
	DefaultMax      = 30 * time.Second
	DefaultLogEvery = 30 * time.Second
)

// Options configures Until. The zero value is usable, and retries nothing.
type Options struct {
	// Transient reports whether an error is worth waiting out. Unset, none is.
	Transient func(error) bool
	// What names what is waited for, in log lines ("OpenCloud").
	What string
	// Initial is the first pause; it doubles up to Max.
	Initial, Max time.Duration
	// LogEvery throttles the log: the first failure is logged, then at most
	// one line per LogEvery, however short the pauses are.
	LogEvery time.Duration
	// Logger receives the waiting and recovery lines.
	Logger *slog.Logger
	// Now and Sleep are injected by tests.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
}

// Until calls attempt until it succeeds, fails with an error that is not
// transient, or ctx ends.
//
// When ctx ends while waiting, the error wraps both ctx's error and the last
// attempt's, so the caller can tell a shutdown (context.Canceled) from a
// deadline and still say what was failing.
func Until(ctx context.Context, attempt func(context.Context) error, opts Options) error {
	opts = opts.withDefaults()
	start := opts.Now()
	var lastLog time.Time
	delay := opts.Initial
	for failures := 0; ; failures++ {
		err := attempt(ctx)
		if err == nil {
			if failures > 0 {
				opts.Logger.Info(opts.What+" answered", "waited", opts.Now().Sub(start).Round(time.Second).String())
			}
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("%w (last error: %w)", ctx.Err(), err)
		}
		if !opts.Transient(err) {
			return err
		}
		if now := opts.Now(); failures == 0 || now.Sub(lastLog) >= opts.LogEvery {
			opts.Logger.Warn("waiting for "+opts.What,
				"err", err, "waited", now.Sub(start).Round(time.Second).String())
			lastLog = now
		}
		if sleepErr := opts.Sleep(ctx, delay); sleepErr != nil {
			return fmt.Errorf("%w (last error: %w)", sleepErr, err)
		}
		delay = min(2*delay, opts.Max)
	}
}

func (o Options) withDefaults() Options {
	if o.Transient == nil {
		o.Transient = func(error) bool { return false }
	}
	if o.What == "" {
		o.What = "a dependency"
	}
	if o.Initial <= 0 {
		o.Initial = DefaultInitial
	}
	if o.Max <= 0 {
		o.Max = DefaultMax
	}
	o.Max = max(o.Max, o.Initial)
	if o.LogEvery <= 0 {
		o.LogEvery = DefaultLogEvery
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = sleep
	}
	return o
}

// sleep waits d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
