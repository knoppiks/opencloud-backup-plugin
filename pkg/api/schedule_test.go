package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/notify"
	"opencloud-backup-plugin/pkg/scheduler"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/state"
)

var apiEpoch = time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

// fakeAdvisor stands in for the scheduler when only the reported next-run time
// matters.
type fakeAdvisor struct {
	next time.Time
	err  error
}

func (f fakeAdvisor) NextRun(context.Context, string) (time.Time, error) { return f.next, f.err }

// scheduleEnv extends the backup environment with the Phase-6 collaborators.
type scheduleEnv struct {
	*backupTestEnv
	events *notify.StateStore
	clock  *testutil.FakeClock
}

func newScheduleEnv(t *testing.T, advisor scheduleAdvisor) *scheduleEnv {
	t.Helper()

	base := newBackupTestEnv(t)
	clock := testutil.NewFakeClock(apiEpoch)
	events := notify.NewStateStore(state.NewMemoryStore(), clock)

	base.srv = NewServer(
		WithTokenValidator(fakeValidator{tokens: map[string]string{"alice-tok": "alice", "bob-tok": "bob"}}),
		WithSpaceReader(fakeSpaceReader{spaces: testSpaces()}),
		WithAuthorizer(base.targets),
		WithSpaceConfigStore(base.configs),
		WithJobStore(base.jobs),
		WithBackupRunner(base.runner),
		WithScheduleAdvisor(advisor),
		WithNotificationStore(events),
	)
	return &scheduleEnv{backupTestEnv: base, events: events, clock: clock}
}

// configure binds the caller's Space to the granted target.
func (e *scheduleEnv) configure(t *testing.T, schedule string, enabled bool) {
	t.Helper()
	if _, err := e.configs.Put(context.Background(), spacecfg.Config{
		SpaceID:  "space-alice",
		TargetID: "t-granted",
		Schedule: schedule,
		Enabled:  enabled,
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
}

func TestPutSchedule_AcceptsAPreset(t *testing.T) {
	env := newScheduleEnv(t, fakeAdvisor{})
	env.configure(t, "", false)

	body := []byte(`{"enabled":true,"preset":{"kind":"daily","hour":2,"minute":30}}`)
	rec := doJSON(env.srv, http.MethodPut, "/api/v1/spaces/space-alice/backup/schedule", "alice-tok", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	var got scheduleResponse
	decodeBody(t, rec.Body, &got)
	if got.Cron != "30 2 * * *" || !got.Enabled {
		t.Fatalf("response = %+v", got)
	}
	if got.Preset.Kind != scheduler.PresetDaily || got.Preset.Hour != 2 || got.Preset.Minute != 30 {
		t.Fatalf("preset = %+v", got.Preset)
	}

	stored, err := env.configs.Get(context.Background(), "space-alice")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Schedule != "30 2 * * *" || !stored.Enabled {
		t.Fatalf("stored = %+v", stored)
	}
	// Setting a schedule must not disturb the target binding.
	if stored.TargetID != "t-granted" {
		t.Fatalf("target binding changed: %+v", stored)
	}
}

func TestPutSchedule_AcceptsCron(t *testing.T) {
	env := newScheduleEnv(t, fakeAdvisor{})
	env.configure(t, "", false)

	body := []byte(`{"enabled":true,"cron":"*/30 * * * *"}`)
	rec := doJSON(env.srv, http.MethodPut, "/api/v1/spaces/space-alice/backup/schedule", "alice-tok", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	var got scheduleResponse
	decodeBody(t, rec.Body, &got)
	if got.Cron != "*/30 * * * *" {
		t.Fatalf("response = %+v", got)
	}
	if got.Preset.Kind != scheduler.PresetCustom {
		t.Fatalf("a cron no preset expresses must report custom: %+v", got.Preset)
	}
}

func TestPutSchedule_RejectsNonsense(t *testing.T) {
	env := newScheduleEnv(t, fakeAdvisor{})
	env.configure(t, "", false)

	for _, body := range []string{
		`{"enabled":true,"cron":"not a schedule"}`,
		`{"enabled":true,"preset":{"kind":"daily","hour":99}}`,
		`{"enabled":true,"preset":{"kind":"hourly"}}`,
		`not json`,
	} {
		rec := doJSON(env.srv, http.MethodPut, "/api/v1/spaces/space-alice/backup/schedule", "alice-tok", []byte(body))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400", body, rec.Code)
		}
	}
}

// A schedule must not become a second way to configure backup: the target
// binding (and its grant check) has to exist first.
func TestPutSchedule_RequiresAConfiguredSpace(t *testing.T) {
	env := newScheduleEnv(t, fakeAdvisor{})

	body := []byte(`{"enabled":true,"preset":{"kind":"daily","hour":2,"minute":30}}`)
	rec := doJSON(env.srv, http.MethodPut, "/api/v1/spaces/space-alice/backup/schedule", "alice-tok", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestSchedule_IsMemberGated(t *testing.T) {
	env := newScheduleEnv(t, fakeAdvisor{})
	env.configure(t, "30 2 * * *", true)

	cases := []struct {
		method string
		path   string
		body   []byte
	}{
		{http.MethodGet, "/api/v1/spaces/space-alice/backup/schedule", nil},
		{http.MethodPut, "/api/v1/spaces/space-alice/backup/schedule", []byte(`{"enabled":true}`)},
		{http.MethodGet, "/api/v1/spaces/space-alice/backup/status", nil},
		{http.MethodGet, "/api/v1/spaces/space-alice/backup/notifications", nil},
	}
	for _, tc := range cases {
		// bob is not a member of alice's space.
		rec := doJSON(env.srv, tc.method, tc.path, "bob-tok", tc.body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s as a non-member: status = %d, want 403", tc.method, tc.path, rec.Code)
		}
		// And an unauthenticated caller gets nothing either.
		rec = doJSON(env.srv, tc.method, tc.path, "", tc.body)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s unauthenticated: status = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

func TestBackupStatus_ReportsHistoryAndNextRun(t *testing.T) {
	next := apiEpoch.Add(12 * time.Hour)
	env := newScheduleEnv(t, fakeAdvisor{next: next})
	env.configure(t, "30 2 * * *", true)
	ctx := context.Background()

	failed, err := env.jobs.Create(ctx, jobs.Job{SpaceID: "space-alice", Kind: jobs.KindBackup, State: jobs.StateRunning})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := env.jobs.Finish(ctx, failed.ID, jobs.Outcome{
		State: jobs.StateFailed,
		Error: "the backup target is unavailable",
	}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	rec := doJSON(env.srv, http.MethodGet, "/api/v1/spaces/space-alice/backup/status", "alice-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	var got statusResponse
	decodeBody(t, rec.Body, &got)
	if !got.Configured || !got.Enabled || got.Cron != "30 2 * * *" {
		t.Fatalf("status = %+v", got)
	}
	if got.Running {
		t.Fatalf("no run is in flight: %+v", got)
	}
	if got.LastRun == nil || got.LastRun.ID != failed.ID {
		t.Fatalf("last run = %+v", got.LastRun)
	}
	if got.LastSuccess != nil {
		t.Fatalf("there has been no successful run: %+v", got.LastSuccess)
	}
	if got.LastError != "the backup target is unavailable" {
		t.Fatalf("last error = %q", got.LastError)
	}
	if got.NextRun != formatTime(next) {
		t.Fatalf("next run = %q, want %q", got.NextRun, formatTime(next))
	}
}

func TestBackupStatus_ReportsARunInFlight(t *testing.T) {
	env := newScheduleEnv(t, fakeAdvisor{})
	env.configure(t, "30 2 * * *", true)

	running, err := env.jobs.Create(context.Background(), jobs.Job{
		SpaceID: "space-alice",
		Kind:    jobs.KindBackup,
		State:   jobs.StateRunning,
		Trigger: jobs.TriggerSchedule,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := doJSON(env.srv, http.MethodGet, "/api/v1/spaces/space-alice/backup/status", "alice-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var got statusResponse
	decodeBody(t, rec.Body, &got)
	if !got.Running || got.CurrentJob == nil || got.CurrentJob.ID != running.ID {
		t.Fatalf("status = %+v", got)
	}
	if got.CurrentJob.Trigger != string(jobs.TriggerSchedule) {
		t.Fatalf("trigger = %q, want the run to be marked as scheduled", got.CurrentJob.Trigger)
	}
}

// An unconfigured Space is a normal answer, not an error: the UI shows "not set
// up yet" rather than a failure.
func TestBackupStatus_UnconfiguredSpace(t *testing.T) {
	env := newScheduleEnv(t, fakeAdvisor{})

	rec := doJSON(env.srv, http.MethodGet, "/api/v1/spaces/space-alice/backup/status", "alice-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var got statusResponse
	decodeBody(t, rec.Body, &got)
	if got.Configured || got.Enabled || got.NextRun != "" {
		t.Fatalf("status = %+v", got)
	}
}

func TestListRuns_HonoursLimit(t *testing.T) {
	env := newScheduleEnv(t, fakeAdvisor{})
	ctx := context.Background()

	for range 5 {
		if _, err := env.jobs.Create(ctx, jobs.Job{SpaceID: "space-alice", Kind: jobs.KindBackup}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	rec := doJSON(env.srv, http.MethodGet, "/api/v1/spaces/space-alice/backup/runs?limit=2", "alice-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got struct {
		Runs []jobResponse `json:"runs"`
	}
	decodeBody(t, rec.Body, &got)
	if len(got.Runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(got.Runs))
	}

	for _, bad := range []string{"0", "-1", "many"} {
		rec := doJSON(env.srv, http.MethodGet, "/api/v1/spaces/space-alice/backup/runs?limit="+bad, "alice-tok", nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("limit=%s: status = %d, want 400", bad, rec.Code)
		}
	}
}

func TestListNotifications_ReturnsOnlyTheSpacesOwn(t *testing.T) {
	env := newScheduleEnv(t, fakeAdvisor{})
	ctx := context.Background()

	if _, err := env.events.Append(ctx, notify.Event{
		Kind:     notify.KindBackupStale,
		Audience: notify.AudienceSpaceMembers,
		SpaceID:  "space-alice",
		Message:  "This space has had no successful backup.",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	env.clock.Advance(time.Minute)
	if _, err := env.events.Append(ctx, notify.Event{
		Kind:     notify.KindTargetUnavailable,
		Audience: notify.AudienceOperator,
		Message:  "A backup target could not be used.",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	rec := doJSON(env.srv, http.MethodGet, "/api/v1/spaces/space-alice/backup/notifications", "alice-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	var got struct {
		Notifications []struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		} `json:"notifications"`
	}
	decodeBody(t, rec.Body, &got)
	if len(got.Notifications) != 1 {
		t.Fatalf("notifications = %+v, want only the space's own", got.Notifications)
	}
	if got.Notifications[0].Kind != string(notify.KindBackupStale) {
		t.Fatalf("notification = %+v", got.Notifications[0])
	}
	// The operator's event must not be reachable through a space-scoped route.
	if contains(rec.Body.String(), "A backup target could not be used.") {
		t.Fatalf("operator event leaked into a member response: %s", rec.Body)
	}
}

// Endpoints must fail closed when their collaborators are not wired.
func TestScheduleEndpoints_FailClosedWithoutCollaborators(t *testing.T) {
	srv := NewServer(
		WithTokenValidator(fakeValidator{tokens: map[string]string{"alice-tok": "alice"}}),
		WithSpaceReader(fakeSpaceReader{spaces: testSpaces()}),
	)

	for _, path := range []string{
		"/api/v1/spaces/space-alice/backup/status",
		"/api/v1/spaces/space-alice/backup/schedule",
		"/api/v1/spaces/space-alice/backup/notifications",
	} {
		rec := doJSON(srv, http.MethodGet, path, "alice-tok", nil)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: status = %d, want 503", path, rec.Code)
		}
	}
}

// testSpaces mirrors the fixtures the backup handler tests use.
func testSpaces() []cs3.Space {
	return []cs3.Space{
		{ID: "space-alice", Name: "Alice", Type: "personal", Owner: "alice"},
		{ID: "space-bob", Name: "Bob", Type: "personal", Owner: "bob"},
	}
}
