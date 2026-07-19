package scheduler

import (
	"testing"
	"time"
)

func TestSystemClockAdvances(t *testing.T) {
	c := SystemClock()
	t0 := c.Now()
	if t0.IsZero() {
		t.Fatal("SystemClock returned zero time")
	}
	if got := c.Now(); got.Before(t0) {
		t.Fatalf("time went backwards: %v < %v", got, t0)
	}
	_ = time.Second
}
