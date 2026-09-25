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

// The UI labels a preset's time with this name, so it must be the zone the
// scheduler actually reads presets in, and never Go's meaningless "Local".
func TestTimezoneNamesTheScheduleZone(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	unnamed := time.FixedZone("Local", 3600)

	for _, tc := range []struct {
		name     string
		location *time.Location
		want     string
	}{
		{"named zone", berlin, "Europe/Berlin"},
		{"default is UTC", nil, "UTC"},
		{"zone read from /etc/localtime has no name", unnamed, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Options{Location: tc.location})
			if got := h.sched.Timezone(); got != tc.want {
				t.Fatalf("Timezone() = %q, want %q", got, tc.want)
			}
		})
	}
}
