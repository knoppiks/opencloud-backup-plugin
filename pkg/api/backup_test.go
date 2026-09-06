package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/backup"
	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/targets"
)

// fakeRunner records StartBackup calls and returns a canned answer.
type fakeRunner struct {
	jobID string
	err   error
	// calls records the space ids the handler asked to back up.
	calls []string
}

func (f *fakeRunner) StartBackup(_ context.Context, spaceID string) (string, error) {
	f.calls = append(f.calls, spaceID)
	if f.err != nil {
		return "", f.err
	}
	return f.jobID, nil
}

// backupTestEnv wires a server with configuration, grant, run and job plumbing.
type backupTestEnv struct {
	srv     *Server
	configs *spacecfg.MemoryStore
	targets *targets.MemoryStore
	jobs    *jobs.MemoryStore
	runner  *fakeRunner
}

func newBackupTestEnv(t *testing.T) *backupTestEnv {
	t.Helper()
	ctx := context.Background()

	val := fakeValidator{tokens: map[string]string{
		"alice-tok": "alice",
		"bob-tok":   "bob",
	}}
	reader := fakeSpaceReader{spaces: []cs3.Space{
		{ID: "space-alice", Name: "Alice", Type: "personal", Owner: "alice"},
		{ID: "space-bob", Name: "Bob", Type: "personal", Owner: "bob"},
	}}

	targetStore := targets.NewMemoryStore()
	if _, err := targetStore.CreateTarget(ctx, targets.Target{ID: "t-granted", Name: "Buddy S3"}); err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	if _, err := targetStore.CreateTarget(ctx, targets.Target{ID: "t-secret", Name: "Someone else's"}); err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	// Only t-granted is granted, and only to alice.
	if err := targetStore.PutGrant(ctx, targets.Grant{
		TargetID: "t-granted", Scope: targets.ScopeUser, UserSub: "alice",
	}); err != nil {
		t.Fatalf("PutGrant: %v", err)
	}

	env := &backupTestEnv{
		configs: spacecfg.NewMemoryStore(),
		targets: targetStore,
		jobs:    jobs.NewMemoryStore(),
		runner:  &fakeRunner{jobID: "job-1"},
	}
	env.srv = NewServer(
		WithTokenValidator(val),
		WithSpaceReader(reader),
		WithAuthorizer(targetStore),
		WithSpaceConfigStore(env.configs),
		WithJobStore(env.jobs),
		WithBackupRunner(env.runner),
	)
	return env
}

func TestPutBackupConfig_StoresGrantedTarget(t *testing.T) {
	env := newBackupTestEnv(t)

	body := []byte(`{"target_id":"t-granted","retention_days":30,"enabled":true}`)
	rec := doJSON(env.srv, http.MethodPut, "/api/v1/spaces/space-alice/backup/config", "alice-tok", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	var got configResponse
	decodeBody(t, rec.Body, &got)
	if got.TargetID != "t-granted" || got.RetentionDays != 30 || !got.Enabled {
		t.Fatalf("response = %+v", got)
	}

	stored, err := env.configs.Get(context.Background(), "space-alice")
	if err != nil {
		t.Fatalf("Get config: %v", err)
	}
	if stored.TargetID != "t-granted" || stored.RetentionWindow != 30*24*time.Hour {
		t.Fatalf("stored config = %+v", stored)
	}
}

// A client naming a target that is not granted to them must be refused, and the
// refusal must not distinguish "not granted" from "no such target".
func TestPutBackupConfig_RejectsUngrantedTarget(t *testing.T) {
	env := newBackupTestEnv(t)

	for _, targetID := range []string{"t-secret", "t-does-not-exist"} {
		body := []byte(`{"target_id":"` + targetID + `"}`)
		rec := doJSON(env.srv, http.MethodPut, "/api/v1/spaces/space-alice/backup/config", "alice-tok", body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("target %q: status = %d, want 403", targetID, rec.Code)
		}
		var body2 errorBody
		decodeBody(t, rec.Body, &body2)
		if body2.Error.Code != "forbidden" {
			t.Fatalf("target %q: error code = %q", targetID, body2.Error.Code)
		}
	}

	if _, err := env.configs.Get(context.Background(), "space-alice"); err == nil {
		t.Fatal("a rejected binding must not be stored")
	}
}

func TestPutBackupConfig_Validation(t *testing.T) {
	env := newBackupTestEnv(t)
	path := "/api/v1/spaces/space-alice/backup/config"

	cases := map[string][]byte{
		"malformed body":  []byte(`{`),
		"missing target":  []byte(`{"retention_days":10}`),
		"negative window": []byte(`{"target_id":"t-granted","retention_days":-1}`),
	}
	for name, body := range cases {
		rec := doJSON(env.srv, http.MethodPut, path, "alice-tok", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", name, rec.Code)
		}
	}
}

func TestGetBackupConfig_RoundTripAndDefaults(t *testing.T) {
	env := newBackupTestEnv(t)
	path := "/api/v1/spaces/space-alice/backup/config"

	if rec := doJSON(env.srv, http.MethodGet, path, "alice-tok", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unconfigured space status = %d, want 404", rec.Code)
	}

	// retention_days omitted -> the deep default is reported back.
	body := []byte(`{"target_id":"t-granted"}`)
	if rec := doJSON(env.srv, http.MethodPut, path, "alice-tok", body); rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body)
	}

	rec := doJSON(env.srv, http.MethodGet, path, "alice-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec.Code)
	}
	var got configResponse
	decodeBody(t, rec.Body, &got)
	if got.RetentionDays != int(spacecfg.DefaultRetentionWindow.Hours()/24) {
		t.Fatalf("retention_days = %d, want the default", got.RetentionDays)
	}
	if got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Fatalf("timestamps missing: %+v", got)
	}
}

// Every backup route is space-scoped: a non-member gets 403 and learns nothing.
func TestBackupRoutes_EnforceSpaceMembership(t *testing.T) {
	env := newBackupTestEnv(t)

	type call struct {
		method, path string
		body         []byte
	}
	calls := []call{
		{http.MethodGet, "/api/v1/spaces/space-bob/backup/config", nil},
		{http.MethodPut, "/api/v1/spaces/space-bob/backup/config", []byte(`{"target_id":"t-granted"}`)},
		{http.MethodPost, "/api/v1/spaces/space-bob/backup/run", nil},
		{http.MethodGet, "/api/v1/spaces/space-bob/backup/runs", nil},
		// An entirely unknown space must be indistinguishable from a foreign one.
		{http.MethodPost, "/api/v1/spaces/space-nope/backup/run", nil},
	}
	for _, c := range calls {
		rec := doJSON(env.srv, c.method, c.path, "alice-tok", c.body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s: status = %d, want 403", c.method, c.path, rec.Code)
		}
	}
	if len(env.runner.calls) != 0 {
		t.Fatalf("runner was invoked for a foreign space: %v", env.runner.calls)
	}
}

func TestBackupRoutes_RequireAuthentication(t *testing.T) {
	env := newBackupTestEnv(t)
	for _, path := range []string{
		"/api/v1/spaces/space-alice/backup/config",
		"/api/v1/spaces/space-alice/backup/runs",
	} {
		if rec := doJSON(env.srv, http.MethodGet, path, "", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", path, rec.Code)
		}
	}
	if rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/backup/run", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("run status = %d, want 401", rec.Code)
	}
}

func TestRunBackup_Accepted(t *testing.T) {
	env := newBackupTestEnv(t)

	rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/backup/run", "alice-tok", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var got runResponse
	decodeBody(t, rec.Body, &got)
	if got.JobID != "job-1" || got.SpaceID != "space-alice" || got.State != string(jobs.StateRunning) {
		t.Fatalf("response = %+v", got)
	}
	if len(env.runner.calls) != 1 || env.runner.calls[0] != "space-alice" {
		t.Fatalf("runner calls = %v", env.runner.calls)
	}
}

func TestRunBackup_MapsRunnerErrors(t *testing.T) {
	cases := []struct {
		err      error
		status   int
		wantCode string
	}{
		{backup.ErrNotConfigured, http.StatusConflict, "not_configured"},
		{backup.ErrTargetUnavailable, http.StatusConflict, "target_unavailable"},
		{backup.ErrRunInProgress, http.StatusConflict, "run_in_progress"},
		{backup.ErrSpaceNotFound, http.StatusNotFound, "not_found"},
		{errors.New("s3: dial tcp garage.internal:3900: refused"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tc := range cases {
		env := newBackupTestEnv(t)
		env.runner.err = tc.err

		rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/backup/run", "alice-tok", nil)
		if rec.Code != tc.status {
			t.Fatalf("%v: status = %d, want %d", tc.err, rec.Code, tc.status)
		}
		var body errorBody
		decodeBody(t, rec.Body, &body)
		if body.Error.Code != tc.wantCode {
			t.Fatalf("%v: code = %q, want %q", tc.err, body.Error.Code, tc.wantCode)
		}
		if want := "garage.internal"; contains(rec.Body.String(), want) {
			t.Fatalf("response leaked internal detail: %s", rec.Body)
		}
	}
}

func TestListRuns_ReturnsHistoryNewestFirst(t *testing.T) {
	ctx := context.Background()
	env := newBackupTestEnv(t)

	first, err := env.jobs.Create(ctx, jobs.Job{SpaceID: "space-alice", Kind: jobs.KindBackup, State: jobs.StateRunning})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := env.jobs.Finish(ctx, first.ID, jobs.Outcome{State: jobs.StateSucceeded, SnapshotID: "snap-1"}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if _, err := env.jobs.Create(ctx, jobs.Job{SpaceID: "space-bob", Kind: jobs.KindBackup}); err != nil {
		t.Fatalf("Create other: %v", err)
	}

	rec := doJSON(env.srv, http.MethodGet, "/api/v1/spaces/space-alice/backup/runs", "alice-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got struct {
		Runs []jobResponse `json:"runs"`
	}
	decodeBody(t, rec.Body, &got)
	if len(got.Runs) != 1 {
		t.Fatalf("runs = %+v, want exactly the caller's space", got.Runs)
	}
	if got.Runs[0].ID != first.ID || got.Runs[0].SnapshotID != "snap-1" {
		t.Fatalf("run = %+v", got.Runs[0])
	}
	if got.Runs[0].State != string(jobs.StateSucceeded) || got.Runs[0].Kind != string(jobs.KindBackup) {
		t.Fatalf("run = %+v", got.Runs[0])
	}
}

func TestBackupRoutes_UnavailableWithoutBackends(t *testing.T) {
	val := fakeValidator{tokens: map[string]string{"alice-tok": "alice"}}
	reader := fakeSpaceReader{spaces: []cs3.Space{
		{ID: "space-alice", Type: "personal", Owner: "alice"},
	}}
	srv := NewServer(WithTokenValidator(val), WithSpaceReader(reader))

	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/spaces/space-alice/backup/config"},
		{http.MethodPut, "/api/v1/spaces/space-alice/backup/config"},
		{http.MethodPost, "/api/v1/spaces/space-alice/backup/run"},
		{http.MethodGet, "/api/v1/spaces/space-alice/backup/runs"},
	}
	for _, c := range cases {
		rec := doJSON(srv, c.method, c.path, "alice-tok", []byte(`{"target_id":"t"}`))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: status = %d, want 503", c.method, c.path, rec.Code)
		}
	}
}

func TestConfigResponse_CarriesNoSecrets(t *testing.T) {
	// The projection must expose neither credentials nor target addressing.
	raw, err := json.Marshal(toConfigResponse(spacecfg.Config{
		SpaceID: "s", TargetID: "t",
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"endpoint", "bucket", "access_key", "secret"} {
		if contains(string(raw), forbidden) {
			t.Fatalf("config response exposes %q: %s", forbidden, raw)
		}
	}
}
