package scheduler

import (
	"fmt"
	"time"

	"opencloud-backup-plugin/internal/config"
	"opencloud-backup-plugin/pkg/jobs"
)

// day is the unit JOB_HISTORY_DAYS counts in.
const day = 24 * time.Hour

// Env is the scheduler's tuning, as the environment declares it
// (internal/config).
type Env struct {
	MaxConcurrent      int    `env:"SCHEDULER_MAX_CONCURRENT" doc:"Scheduled runs at the same time."`
	JobHistoryDays     int    `env:"JOB_HISTORY_DAYS" doc:"Days finished runs and notifications are kept."`
	PruneIntervalHours int    `env:"PRUNE_INTERVAL_HOURS" doc:"How often each Space's retention is applied. How much is kept is the Space's own setting."`
	Timezone           string `env:"SCHEDULE_TIMEZONE" doc:"IANA zone schedules are read in, such as Europe/Berlin. Unset, the container's zone (TZ)."`

	// location is Timezone, loaded by Validate.
	location *time.Location
}

// DefaultEnv is the configuration of an unset environment: the scheduler's
// own defaults.
func DefaultEnv() Env {
	return Env{
		MaxConcurrent:      DefaultMaxConcurrent,
		JobHistoryDays:     int(jobs.DefaultHistoryWindow / day),
		PruneIntervalHours: int(DefaultPruneInterval / time.Hour),
	}
}

var _ config.Validator = (*Env)(nil)

// Validate loads the timezone.
func (e *Env) Validate() error {
	e.location = nil
	if e.Timezone == "" {
		return nil
	}
	loc, err := time.LoadLocation(e.Timezone)
	if err != nil {
		return fmt.Errorf("SCHEDULE_TIMEZONE %q is not a known timezone", e.Timezone)
	}
	e.location = loc
	return nil
}

// Location is the zone schedules are read in: SCHEDULE_TIMEZONE, or the
// container's own zone. "Nightly at half past two" means the family's night,
// and a deployment that sets TZ for its logs has already said which zone it
// thinks in.
func (e Env) Location() *time.Location {
	if e.location != nil {
		return e.location
	}
	return time.Local
}

// Options is the scheduler's tuning from e.
func (e Env) Options() Options {
	return Options{
		MaxConcurrent: e.MaxConcurrent,
		HistoryWindow: time.Duration(e.JobHistoryDays) * day,
		// How often retention is *applied*. How much is kept is the Space
		// owner's setting; this is the operator's.
		PruneInterval: time.Duration(e.PruneIntervalHours) * time.Hour,
		Location:      e.Location(),
	}
}
