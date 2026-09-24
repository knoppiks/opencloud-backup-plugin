package notify

import (
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/spacecfg"
)

func succeededAt(at time.Time) jobs.Job {
	return jobs.Job{Kind: jobs.KindBackup, State: jobs.StateSucceeded, CreatedAt: at}
}

func failedAt(at time.Time) jobs.Job {
	return jobs.Job{Kind: jobs.KindBackup, State: jobs.StateFailed, CreatedAt: at}
}

func nightly(created time.Time) spacecfg.Config {
	return spacecfg.Config{SpaceID: "s1", Schedule: "30 2 * * *", Enabled: true, CreatedAt: created}
}

func TestStaleRule_MeasuresFromTheLastSuccess(t *testing.T) {
	last := epoch
	// Newest first: a failure after the success must not reset the clock.
	backups := []jobs.Job{failedAt(last.Add(24 * time.Hour)), succeededAt(last)}
	cfg := nightly(epoch.Add(-30 * 24 * time.Hour))

	got := StaleRule{}.Assess(cfg, backups, last.Add(47*time.Hour))
	if got.Stale || !got.Since.Equal(last) {
		t.Fatalf("one missed night: %+v", got)
	}
	got = StaleRule{}.Assess(cfg, backups, last.Add(49*time.Hour))
	if !got.Stale || !got.Since.Equal(last) {
		t.Fatalf("two missed nights: %+v", got)
	}
}

// A Space that has never succeeded is measured from when it was configured, so
// a setup that never worked is caught rather than waiting forever.
func TestStaleRule_NeverSucceededMeasuresFromConfiguration(t *testing.T) {
	cfg := nightly(epoch)
	got := StaleRule{}.Assess(cfg, []jobs.Job{failedAt(epoch.Add(time.Hour))}, epoch.Add(72*time.Hour))
	if !got.Stale || !got.Since.Equal(epoch) {
		t.Fatalf("never succeeded: %+v", got)
	}
}

func TestStaleRule_DisabledIsNeverStale(t *testing.T) {
	cfg := nightly(epoch)
	cfg.Enabled = false
	if got := (StaleRule{}).Assess(cfg, nil, epoch.Add(365*24*time.Hour)); got.Stale {
		t.Fatalf("disabled space reported stale: %+v", got)
	}
}

func TestStaleRule_NothingToMeasureFrom(t *testing.T) {
	cfg := nightly(time.Time{})
	if got := (StaleRule{}).Assess(cfg, nil, epoch); got.Stale || !got.Since.IsZero() {
		t.Fatalf("no baseline: %+v", got)
	}
}

func TestStaleRule_ThresholdFollowsTheSchedule(t *testing.T) {
	cfg := spacecfg.Config{SpaceID: "w", Schedule: "0 3 * * 0", Enabled: true, CreatedAt: epoch}
	backups := []jobs.Job{succeededAt(epoch)}
	if got := (StaleRule{}).Assess(cfg, backups, epoch.Add(13*24*time.Hour)); got.Stale {
		t.Fatalf("weekly space stale within two weeks: %+v", got)
	}
	if got := (StaleRule{}).Assess(cfg, backups, epoch.Add(15*24*time.Hour)); !got.Stale {
		t.Fatalf("weekly space not stale after two weeks: %+v", got)
	}
}

// The floor applies to frequent schedules: a run every half hour does not make
// a Space stale an hour after its last success.
func TestStaleRule_FloorAppliesToFrequentSchedules(t *testing.T) {
	cfg := spacecfg.Config{SpaceID: "f", Schedule: "*/30 * * * *", Enabled: true, CreatedAt: epoch}
	backups := []jobs.Job{succeededAt(epoch)}
	if got := (StaleRule{}).Assess(cfg, backups, epoch.Add(DefaultMinStaleAfter-time.Minute)); got.Stale {
		t.Fatalf("stale below the default floor: %+v", got)
	}
	rule := StaleRule{MinStaleAfter: 2 * time.Hour}
	if got := rule.Assess(cfg, backups, epoch.Add(3*time.Hour)); !got.Stale {
		t.Fatalf("configured floor ignored: %+v", got)
	}
}
