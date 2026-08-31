package testutil

import (
	"sync"
	"time"
)

// FakeClock is a deterministic clock for tests. It satisfies the Clock
// interface used across the packages (Now() time.Time) without importing any of
// them, avoiding an import cycle between testutil and its consumers.
//
// It is safe for concurrent use: background workers read it while tests advance
// it.
type FakeClock struct {
	mu      sync.RWMutex
	current time.Time
}

// NewFakeClock returns a FakeClock pinned to start.
func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{current: start}
}

// Now returns the current fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.current
}

// Advance moves the fake clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(d)
}

// Set pins the fake clock to t.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = t
}
