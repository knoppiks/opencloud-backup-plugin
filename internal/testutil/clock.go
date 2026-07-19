package testutil

import "time"

// FakeClock is a deterministic scheduler.Clock for tests. It implements the
// scheduler.Clock interface (Now() time.Time) without importing the scheduler
// package, avoiding an import cycle between testutil and consumers.
type FakeClock struct {
	current time.Time
}

// NewFakeClock returns a FakeClock pinned to start.
func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{current: start}
}

// Now returns the current fake time.
func (c *FakeClock) Now() time.Time { return c.current }

// Advance moves the fake clock forward by d.
func (c *FakeClock) Advance(d time.Duration) { c.current = c.current.Add(d) }

// Set pins the fake clock to t.
func (c *FakeClock) Set(t time.Time) { c.current = t }
