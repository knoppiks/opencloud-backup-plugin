package testutil

import (
	"testing"
	"time"
)

func TestFakeClockAdvanceAndSet(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewFakeClock(start)

	if !c.Now().Equal(start) {
		t.Fatalf("Now() = %v, want %v", c.Now(), start)
	}

	c.Advance(90 * time.Minute)
	if want := start.Add(90 * time.Minute); !c.Now().Equal(want) {
		t.Fatalf("after Advance Now() = %v, want %v", c.Now(), want)
	}

	c.Set(start)
	if !c.Now().Equal(start) {
		t.Fatalf("after Set Now() = %v, want %v", c.Now(), start)
	}
}
