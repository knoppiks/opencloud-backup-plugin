package retry

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/knoppiks/opencloud-backup-plugin/internal/testutil"
)

var (
	errDown   = errors.New("connection refused")
	errDenied = errors.New("wrong secret")
	epoch     = time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)
)

// harness runs Until with a fake clock whose sleeps advance it, recording the
// pauses taken.
type harness struct {
	clock  *testutil.FakeClock
	pauses []time.Duration
	logs   bytes.Buffer
}

func newHarness() *harness { return &harness{clock: testutil.NewFakeClock(epoch)} }

func (h *harness) options() Options {
	return Options{
		Transient: func(err error) bool { return errors.Is(err, errDown) },
		What:      "OpenCloud",
		Logger:    slog.New(slog.NewTextHandler(&h.logs, nil)),
		Now:       h.clock.Now,
		Sleep: func(ctx context.Context, d time.Duration) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			h.pauses = append(h.pauses, d)
			h.clock.Advance(d)
			return nil
		},
	}
}

// failing returns errs in turn, then succeeds.
func failing(errs ...error) func(context.Context) error {
	return func(context.Context) error {
		if len(errs) == 0 {
			return nil
		}
		err := errs[0]
		errs = errs[1:]
		return err
	}
}

func repeat(err error, n int) []error {
	out := make([]error, n)
	for i := range out {
		out[i] = err
	}
	return out
}

func TestUntil_SucceedsAtOnceWithoutWaiting(t *testing.T) {
	h := newHarness()
	if err := Until(context.Background(), failing(), h.options()); err != nil {
		t.Fatalf("Until = %v", err)
	}
	if len(h.pauses) != 0 || h.logs.Len() != 0 {
		t.Fatalf("pauses %v, logs %q; want neither", h.pauses, h.logs.String())
	}
}

func TestUntil_BacksOffFromOneToThirtySeconds(t *testing.T) {
	h := newHarness()
	if err := Until(context.Background(), failing(repeat(errDown, 8)...), h.options()); err != nil {
		t.Fatalf("Until = %v", err)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	if len(h.pauses) != len(want) {
		t.Fatalf("pauses = %v, want %v seconds", h.pauses, want)
	}
	for i, p := range h.pauses {
		if p != want[i]*time.Second {
			t.Fatalf("pauses = %v, want %v seconds", h.pauses, want)
		}
	}
	if !strings.Contains(h.logs.String(), "OpenCloud answered") {
		t.Errorf("no recovery line in %q", h.logs.String())
	}
}

// The first failure is logged at once, then at most one line per LogEvery,
// however often it is retried.
func TestUntil_ThrottlesTheLog(t *testing.T) {
	h := newHarness()
	opts := h.options()
	opts.Initial, opts.Max = time.Second, time.Second
	// 95 failures, one second apart: lines at 0s, 30s, 60s, 90s.
	if err := Until(context.Background(), failing(repeat(errDown, 95)...), opts); err != nil {
		t.Fatalf("Until = %v", err)
	}
	if got := strings.Count(h.logs.String(), "waiting for OpenCloud"); got != 4 {
		t.Fatalf("%d waiting lines, want 4:\n%s", got, h.logs.String())
	}
}

// A misconfiguration is not waited on.
func TestUntil_ReturnsANonTransientErrorAtOnce(t *testing.T) {
	h := newHarness()
	err := Until(context.Background(), failing(errDown, errDenied), h.options())
	if !errors.Is(err, errDenied) {
		t.Fatalf("Until = %v, want errDenied", err)
	}
	if len(h.pauses) != 1 {
		t.Fatalf("pauses = %v, want one (before the refusal)", h.pauses)
	}
}

// Shutting down while waiting is told apart from a failure, and still says
// what was failing.
func TestUntil_StopsWhenTheContextEnds(t *testing.T) {
	h := newHarness()
	ctx, cancel := context.WithCancel(context.Background())
	opts := h.options()
	sleep := opts.Sleep
	opts.Sleep = func(ctx context.Context, d time.Duration) error {
		if len(h.pauses) == 2 {
			cancel()
		}
		return sleep(ctx, d)
	}
	err := Until(ctx, failing(repeat(errDown, 10)...), opts)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, errDown) {
		t.Fatalf("Until = %v, want context.Canceled and the last error", err)
	}
}

// An attempt that fails because ctx ended reports ctx's error, not as a
// non-transient failure of its own.
func TestUntil_AnAttemptCutOffByTheContext(t *testing.T) {
	h := newHarness()
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	attempt := func(context.Context) error {
		cancel()
		return errDenied
	}
	err := Until(ctx, attempt, h.options())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Until = %v, want context.Canceled", err)
	}
}

func TestUntil_RealSleepHonoursTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleep = %v", err)
	}
	if err := sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("sleep = %v", err)
	}
}

func TestOptionsDefaults(t *testing.T) {
	o := Options{}.withDefaults()
	if o.Initial != DefaultInitial || o.Max != DefaultMax || o.LogEvery != DefaultLogEvery {
		t.Fatalf("defaults = %v/%v/%v", o.Initial, o.Max, o.LogEvery)
	}
	if o.Transient(errDown) {
		t.Fatal("with no classifier nothing is transient")
	}
	if o = (Options{Initial: time.Minute, Max: time.Second}).withDefaults(); o.Max != time.Minute {
		t.Fatalf("Max below Initial = %v, want raised to Initial", o.Max)
	}
}
