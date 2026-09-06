package notify

import (
	"context"
	"errors"
	"testing"
	"time"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/state"
)

type monitorHarness struct {
	t        *testing.T
	clock    *testutil.FakeClock
	configs  *spacecfg.MemoryStore
	jobs     *jobs.MemoryStore
	events   *StateStore
	notifier *Notifier
	monitor  *Monitor
}

func newMonitorHarness(t *testing.T, opts MonitorOptions) *monitorHarness {
	t.Helper()

	clock := testutil.NewFakeClock(epoch)
	events := NewStateStore(state.NewMemoryStore(), clock)
	notifier, err := New(events, Options{Clock: clock})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	h := &monitorHarness{
		t:        t,
		clock:    clock,
		configs:  spacecfg.NewMemoryStoreWithClock(clock),
		jobs:     jobs.NewMemoryStoreWithClock(clock),
		events:   events,
		notifier: notifier,
	}

	monitor, err := NewMonitor(MonitorDeps{
		Configs:  h.configs,
		Jobs:     h.jobs,
		Events:   events,
		Notifier: notifier,
	}, opts)
	if err != nil {
		t.Fatalf("NewMonitor: %v", err)
	}
	h.monitor = monitor
	return h
}

func (h *monitorHarness) configure(spaceID, schedule string, enabled bool) {
	h.t.Helper()
	if _, err := h.configs.Put(context.Background(), spacecfg.Config{
		SpaceID:  spaceID,
		TargetID: "t1",
		Schedule: schedule,
		Enabled:  enabled,
	}); err != nil {
		h.t.Fatalf("configure: %v", err)
	}
}

func (h *monitorHarness) succeed(spaceID string) {
	h.t.Helper()
	ctx := context.Background()
	j, err := h.jobs.Create(ctx, jobs.Job{SpaceID: spaceID, Kind: jobs.KindBackup, State: jobs.StateRunning})
	if err != nil {
		h.t.Fatalf("Create: %v", err)
	}
	if err := h.jobs.Finish(ctx, j.ID, jobs.Outcome{State: jobs.StateSucceeded}); err != nil {
		h.t.Fatalf("Finish: %v", err)
	}
}

func (h *monitorHarness) staleEvents(spaceID string) []Event {
	h.t.Helper()
	all, err := h.events.List(context.Background(), spaceID, 0)
	if err != nil {
		h.t.Fatalf("List: %v", err)
	}
	var out []Event
	for _, e := range all {
		if e.Kind == KindBackupStale {
			out = append(out, e)
		}
	}
	return out
}

func (h *monitorHarness) sweep() {
	h.t.Helper()
	h.monitor.Sweep(context.Background(), h.clock.Now())
}

// The headline case: backups quietly stopped and nobody noticed.
func TestMonitor_ReportsAStaleBackup(t *testing.T) {
	h := newMonitorHarness(t, MonitorOptions{})
	h.configure("s1", "30 2 * * *", true)
	h.succeed("s1")

	// One missed night is not stale yet.
	h.clock.Advance(30 * time.Hour)
	h.sweep()
	if got := h.staleEvents("s1"); len(got) != 0 {
		t.Fatalf("reported stale too early: %+v", got)
	}

	// Two missed nights is.
	h.clock.Advance(30 * time.Hour)
	h.sweep()
	got := h.staleEvents("s1")
	if len(got) != 1 {
		t.Fatalf("stale events = %+v, want exactly one", got)
	}
	if got[0].Audience != AudienceSpaceMembers || got[0].SpaceID != "s1" {
		t.Fatalf("stale event = %+v", got[0])
	}
	if got[0].Message == "" {
		t.Fatal("stale event needs a message")
	}
}

// Weekly Spaces must not be nagged after two days.
func TestMonitor_ThresholdFollowsTheSchedule(t *testing.T) {
	h := newMonitorHarness(t, MonitorOptions{})
	h.configure("weekly", "0 3 * * 0", true)
	h.succeed("weekly")

	h.clock.Advance(8 * 24 * time.Hour)
	h.sweep()
	if got := h.staleEvents("weekly"); len(got) != 0 {
		t.Fatalf("a weekly space was reported stale after one missed week: %+v", got)
	}

	h.clock.Advance(9 * 24 * time.Hour)
	h.sweep()
	if got := h.staleEvents("weekly"); len(got) != 1 {
		t.Fatalf("stale events = %+v, want one after two missed weeks", got)
	}
}

func TestMonitor_DoesNotNag(t *testing.T) {
	h := newMonitorHarness(t, MonitorOptions{RepeatAfter: 7 * 24 * time.Hour})
	h.configure("s1", "30 2 * * *", true)
	h.succeed("s1")

	h.clock.Advance(72 * time.Hour)
	for range 5 {
		h.sweep()
		h.clock.Advance(time.Hour)
	}
	if got := h.staleEvents("s1"); len(got) != 1 {
		t.Fatalf("stale events = %d, want one until the repeat window elapses", len(got))
	}

	// After the repeat window, it says so again — the problem has not gone away.
	h.clock.Advance(8 * 24 * time.Hour)
	h.sweep()
	if got := h.staleEvents("s1"); len(got) != 2 {
		t.Fatalf("stale events = %d, want a repeat after the window", len(got))
	}
}

func TestMonitor_SuccessfulRunClearsStaleness(t *testing.T) {
	h := newMonitorHarness(t, MonitorOptions{})
	h.configure("s1", "30 2 * * *", true)
	h.succeed("s1")

	h.clock.Advance(72 * time.Hour)
	h.sweep()
	if got := h.staleEvents("s1"); len(got) != 1 {
		t.Fatalf("stale events = %+v", got)
	}

	// A run succeeds; time passes but less than the threshold.
	h.succeed("s1")
	h.clock.Advance(30 * time.Hour)
	h.sweep()
	if got := h.staleEvents("s1"); len(got) != 1 {
		t.Fatalf("stale events = %d, want no new report after a success", len(got))
	}
}

// A Space that never ran is measured from when it was configured, so enabling
// backup does not immediately produce a "stale" notification.
func TestMonitor_NeverRunSpaceUsesItsConfigurationTime(t *testing.T) {
	h := newMonitorHarness(t, MonitorOptions{})
	h.configure("s1", "30 2 * * *", true)

	h.clock.Advance(time.Hour)
	h.sweep()
	if got := h.staleEvents("s1"); len(got) != 0 {
		t.Fatalf("a freshly configured space was reported stale: %+v", got)
	}

	h.clock.Advance(72 * time.Hour)
	h.sweep()
	if got := h.staleEvents("s1"); len(got) != 1 {
		t.Fatalf("a space that never ran was never reported: %+v", got)
	}
}

func TestMonitor_IgnoresDisabledSpaces(t *testing.T) {
	h := newMonitorHarness(t, MonitorOptions{})
	h.configure("off", "30 2 * * *", false)

	h.clock.Advance(30 * 24 * time.Hour)
	h.sweep()
	if got := h.staleEvents("off"); len(got) != 0 {
		t.Fatalf("a disabled space was reported stale: %+v", got)
	}
}

func TestNewMonitor_RequiresCollaborators(t *testing.T) {
	clock := testutil.NewFakeClock(epoch)
	events := NewStateStore(state.NewMemoryStore(), clock)
	notifier, err := New(events, Options{Clock: clock})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	full := MonitorDeps{
		Configs:  spacecfg.NewMemoryStore(),
		Jobs:     jobs.NewMemoryStore(),
		Events:   events,
		Notifier: notifier,
	}
	if _, err := NewMonitor(full, MonitorOptions{}); err != nil {
		t.Fatalf("NewMonitor: %v", err)
	}

	for name, mutate := range map[string]func(*MonitorDeps){
		"configs":  func(d *MonitorDeps) { d.Configs = nil },
		"jobs":     func(d *MonitorDeps) { d.Jobs = nil },
		"events":   func(d *MonitorDeps) { d.Events = nil },
		"notifier": func(d *MonitorDeps) { d.Notifier = nil },
	} {
		deps := full
		mutate(&deps)
		if _, err := NewMonitor(deps, MonitorOptions{}); err == nil {
			t.Fatalf("missing %s must be rejected", name)
		}
	}
}

// A failed run tells the member what happened and, when the cause is the
// operator's to fix, tells the operator too — without naming the Space
// (decisions.md #15).
func TestReporter_SplitsAudiences(t *testing.T) {
	clock := testutil.NewFakeClock(epoch)
	events := NewStateStore(state.NewMemoryStore(), clock)
	notifier, err := New(events, Options{Clock: clock})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	targetDown := errors.New("target unavailable")
	reporter := NewReporter(notifier, func(err error) Classification {
		if errors.Is(err, targetDown) {
			return Classification{
				MemberMessage:   "The backup target is unavailable.",
				Operational:     true,
				OperatorMessage: "A backup target could not be used.",
			}
		}
		return DefaultClassification()
	}, nil)

	ctx := context.Background()
	reporter.RunFinished(ctx, "s1", targetDown)

	member, err := events.List(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(member) != 1 || member[0].Kind != KindRunFailed {
		t.Fatalf("member events = %+v", member)
	}

	operator, err := events.ListOperator(ctx, 0)
	if err != nil {
		t.Fatalf("ListOperator: %v", err)
	}
	if len(operator) != 1 || operator[0].Kind != KindTargetUnavailable {
		t.Fatalf("operator events = %+v", operator)
	}
	if operator[0].SpaceID != "" {
		t.Fatalf("operator event names a space: %+v", operator[0])
	}
}

func TestReporter_NonOperationalFailureStaysWithTheMember(t *testing.T) {
	clock := testutil.NewFakeClock(epoch)
	events := NewStateStore(state.NewMemoryStore(), clock)
	notifier, err := New(events, Options{Clock: clock})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	reporter := NewReporter(notifier, nil, nil)

	ctx := context.Background()
	reporter.RunFinished(ctx, "s1", errors.New("some failure"))

	member, err := events.List(ctx, "s1", 0)
	if err != nil || len(member) != 1 {
		t.Fatalf("member events = %+v (%v)", member, err)
	}
	operator, err := events.ListOperator(ctx, 0)
	if err != nil || len(operator) != 0 {
		t.Fatalf("operator events = %+v (%v)", operator, err)
	}
}

// Some failures are not news either: a run skipped because another was already
// going, or one cut short by shutdown.
func TestReporter_SilentClassificationNotifiesNobody(t *testing.T) {
	clock := testutil.NewFakeClock(epoch)
	events := NewStateStore(state.NewMemoryStore(), clock)
	notifier, err := New(events, Options{Clock: clock})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	reporter := NewReporter(notifier, func(error) Classification {
		return Classification{Silent: true, MemberMessage: "ignored", Operational: true}
	}, nil)

	reporter.RunFinished(context.Background(), "s1", errors.New("a run is already in progress"))

	member, err := events.List(context.Background(), "s1", 0)
	if err != nil || len(member) != 0 {
		t.Fatalf("member events = %+v (%v)", member, err)
	}
	operator, err := events.ListOperator(context.Background(), 0)
	if err != nil || len(operator) != 0 {
		t.Fatalf("operator events = %+v (%v)", operator, err)
	}
}

// Backups that work are not news.
func TestReporter_SuccessIsSilent(t *testing.T) {
	clock := testutil.NewFakeClock(epoch)
	events := NewStateStore(state.NewMemoryStore(), clock)
	notifier, err := New(events, Options{Clock: clock})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	reporter := NewReporter(notifier, nil, nil)

	reporter.RunFinished(context.Background(), "s1", nil)

	got, err := events.List(context.Background(), "s1", 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("events after a successful run = %+v (%v)", got, err)
	}
}
