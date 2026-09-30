package cs3

// Reading a file OpenCloud has not finished with.
//
// OpenCloud accepts an upload before it is done with it: post-processing
// (virus scanning, among others) runs afterwards, and until it finishes a read
// is refused with "too early" — HTTP 425 on the data path, CODE_TOO_EARLY on
// the gateway. The file is not missing and nothing is broken; it is not ready.
//
// Such a read is tried again for a bounded period. The bound cuts both ways:
// too short and a freshly uploaded file still fails the night's backup; too
// long and an upload whose processing never finishes holds a run open, which
// the run deadline exists to prevent. It is per file, and that is enough: a
// file that stays unreadable fails the run it is part of, so a run waits out at
// most one full window before it ends.
//
// Exhausting the window stays a failure (ErrNotReady). A backup never quietly
// leaves out a file it could not read.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// DefaultNotReadyWait is how long a read refused as "too early" is retried
// before it fails. It matches DefaultStallTimeout: both are how long OpenCloud
// may go without delivering a file before the service stops waiting for it.
const DefaultNotReadyWait = 2 * time.Minute

const (
	// notReadyFirstDelay is the pause before the first retry. Post-processing
	// without a virus scanner usually finishes within it.
	notReadyFirstDelay = 250 * time.Millisecond
	// notReadyMaxDelay caps the doubling pause, so a file that becomes ready
	// late in the window is not left waiting much longer than it has to.
	notReadyMaxDelay = 5 * time.Second
)

// ErrNotReady is returned when OpenCloud still refused to hand out a file
// because it has not finished processing it, after the retry window ran out.
var ErrNotReady = errors.New("cs3: file is still being processed by OpenCloud")

// WithNotReadyWait sets how long a read of a file OpenCloud has not finished
// processing is retried. Zero or less uses DefaultNotReadyWait.
func WithNotReadyWait(d time.Duration) ClientOption {
	return func(c *Client) { c.notReadyWait = d }
}

// openWhenReady calls open until it returns something other than ErrNotReady,
// or until the retry window or ctx runs out.
func (c *Client) openWhenReady(ctx context.Context, open func() (io.ReadCloser, error)) (io.ReadCloser, error) {
	window := c.notReadyWindow()
	deadline := c.clock().Add(window)
	delay := notReadyFirstDelay
	for {
		rc, err := open()
		if !errors.Is(err, ErrNotReady) {
			return rc, err
		}
		remaining := deadline.Sub(c.clock())
		if remaining <= 0 {
			return nil, fmt.Errorf("cs3 download: gave up after %s: %w", window, err)
		}
		if err := c.pause(ctx, min(delay, remaining)); err != nil {
			return nil, fmt.Errorf("cs3 download: %w: %w", ErrNotReady, err)
		}
		delay = min(2*delay, notReadyMaxDelay)
	}
}

// notReadyWindow is the configured retry window.
func (c *Client) notReadyWindow() time.Duration {
	if c.notReadyWait > 0 {
		return c.notReadyWait
	}
	return DefaultNotReadyWait
}

// clock returns the current time; tests replace it.
func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// pause waits d or until ctx ends; tests replace it.
func (c *Client) pause(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	return sleepCtx(ctx, d)
}

// sleepCtx waits d, returning early with ctx's error if it ends first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
