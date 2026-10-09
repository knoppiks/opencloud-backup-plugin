package scheduler

import (
	"strings"
	"testing"
	"time"

	_ "time/tzdata"

	"opencloud-backup-plugin/internal/config"
	"opencloud-backup-plugin/pkg/jobs"
)

func loadEnv(t *testing.T, environ ...string) (Env, error) {
	t.Helper()
	e := DefaultEnv()
	err := config.Load(environ, &e)
	return e, err
}

// The defaults are the scheduler's own, so the reference tells the truth
// about an unset variable.
func TestEnv_DefaultsAreTheSchedulersOwn(t *testing.T) {
	e, err := loadEnv(t)
	if err != nil {
		t.Fatal(err)
	}
	opts := e.Options()
	if opts.MaxConcurrent != DefaultMaxConcurrent || opts.HistoryWindow != jobs.DefaultHistoryWindow ||
		opts.PruneInterval != DefaultPruneInterval {
		t.Fatalf("Options = %+v", opts)
	}
}

func TestEnv_Options(t *testing.T) {
	e, err := loadEnv(t, "SCHEDULER_MAX_CONCURRENT=4", "JOB_HISTORY_DAYS=30", "PRUNE_INTERVAL_HOURS=6")
	if err != nil {
		t.Fatal(err)
	}
	opts := e.Options()
	if opts.MaxConcurrent != 4 || opts.HistoryWindow != 30*24*time.Hour || opts.PruneInterval != 6*time.Hour {
		t.Fatalf("Options = %+v", opts)
	}
}

// "Nightly at half past two" is about the family's night. The container's own
// zone is the closest thing this process can know, and a deployment that sets
// TZ has already said which zone it thinks in.
func TestEnv_DefaultsToTheContainerTimezone(t *testing.T) {
	setLocal(t, "Europe/Berlin")
	e, err := loadEnv(t)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Options().Location.String(); got != "Europe/Berlin" {
		t.Fatalf("location = %q, want the container's zone", got)
	}
}

func TestEnv_ExplicitTimezoneWins(t *testing.T) {
	setLocal(t, "Europe/Berlin")
	e, err := loadEnv(t, "SCHEDULE_TIMEZONE=America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Options().Location.String(); got != "America/New_York" {
		t.Fatalf("location = %q, want the configured zone", got)
	}
}

func TestEnv_RejectsAnUnknownTimezone(t *testing.T) {
	if _, err := loadEnv(t, "SCHEDULE_TIMEZONE=Middle/Earth"); err == nil || !strings.Contains(err.Error(), "SCHEDULE_TIMEZONE") {
		t.Fatalf("err = %v", err)
	}
}

// setLocal sets what Go would have read from TZ, which it reads once.
func setLocal(t *testing.T, name string) {
	t.Helper()
	original := time.Local
	t.Cleanup(func() { time.Local = original })
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	time.Local = loc
}
