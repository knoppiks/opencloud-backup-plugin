package scheduler

// Schedules: presets on the outside, cron on the inside.
//
// The UI offers "daily at 02:30" and "every Sunday at 03:00", because the user
// this product is built for (decisions.md, product framing) should never have to
// meet a cron expression. Internally there is exactly one representation — a
// cron expression — so the scheduler, the API and the stored configuration all
// reason about the same thing, and an operator who wants something the presets
// cannot express can still send cron through the API.
//
// Cron parsing is github.com/hashicorp/cronexpr (Apache-2.0), which the
// dependency graph already carries via kopia. Writing our own cron parser would
// be the same category of mistake as writing our own crypto: a solved problem
// with subtle edge cases.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/cronexpr"
)

// PresetKind is one of the schedule shapes the UI offers.
type PresetKind string

const (
	// PresetDaily runs once a day at a chosen time.
	PresetDaily PresetKind = "daily"
	// PresetWeekly runs once a week on a chosen weekday and time.
	PresetWeekly PresetKind = "weekly"
	// PresetCustom marks a cron expression no preset describes.
	PresetCustom PresetKind = "custom"
)

// Preset is a family-legible schedule.
type Preset struct {
	Kind PresetKind `json:"kind"`
	// Hour and Minute are the local time of day the run starts.
	Hour   int `json:"hour"`
	Minute int `json:"minute"`
	// Weekday applies to PresetWeekly only.
	Weekday time.Weekday `json:"weekday,omitempty"`
}

// Cron renders the preset as a cron expression.
func (p Preset) Cron() (string, error) {
	if p.Hour < 0 || p.Hour > 23 {
		return "", fmt.Errorf("scheduler: hour must be between 0 and 23")
	}
	if p.Minute < 0 || p.Minute > 59 {
		return "", fmt.Errorf("scheduler: minute must be between 0 and 59")
	}

	switch p.Kind {
	case PresetDaily:
		return fmt.Sprintf("%d %d * * *", p.Minute, p.Hour), nil
	case PresetWeekly:
		if p.Weekday < time.Sunday || p.Weekday > time.Saturday {
			return "", fmt.Errorf("scheduler: weekday must be between 0 (Sunday) and 6")
		}
		return fmt.Sprintf("%d %d * * %d", p.Minute, p.Hour, int(p.Weekday)), nil
	default:
		return "", fmt.Errorf("scheduler: unknown schedule preset")
	}
}

// PresetOf recognises the cron expressions the presets produce, so a schedule
// set through the UI reads back as the same preset. Anything else is custom —
// reported honestly rather than approximated.
func PresetOf(expr string) Preset {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return Preset{Kind: PresetCustom}
	}

	minute, minuteOK := numericField(fields[0], 0, 59)
	hour, hourOK := numericField(fields[1], 0, 23)
	if !minuteOK || !hourOK || fields[2] != "*" || fields[3] != "*" {
		return Preset{Kind: PresetCustom}
	}

	if fields[4] == "*" {
		return Preset{Kind: PresetDaily, Hour: hour, Minute: minute}
	}
	if weekday, ok := numericField(fields[4], 0, 6); ok {
		return Preset{
			Kind:    PresetWeekly,
			Hour:    hour,
			Minute:  minute,
			Weekday: time.Weekday(weekday),
		}
	}
	return Preset{Kind: PresetCustom}
}

// numericField parses a plain numeric cron field within bounds.
func numericField(field string, min, max int) (int, bool) {
	v, err := strconv.Atoi(field)
	if err != nil || v < min || v > max {
		return 0, false
	}
	return v, true
}

// ValidateCron reports whether an expression is a schedule this service can run.
func ValidateCron(expr string) error {
	if strings.TrimSpace(expr) == "" {
		return fmt.Errorf("scheduler: schedule must not be empty")
	}
	if _, err := cronexpr.Parse(expr); err != nil {
		// The parser's message quotes the input; keep the answer generic.
		return fmt.Errorf("scheduler: not a valid schedule")
	}
	return nil
}

// NextAfter returns the first time expr fires strictly after t, evaluated in
// t's location. It returns the zero time when the expression never fires again
// (cron can express dates that do not recur).
func NextAfter(expr string, t time.Time) (time.Time, error) {
	parsed, err := cronexpr.Parse(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("scheduler: not a valid schedule")
	}
	return parsed.Next(t), nil
}
