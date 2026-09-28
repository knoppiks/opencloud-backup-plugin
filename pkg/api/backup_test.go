package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
		// Retention depth is the defence against ransomware; a member session
		// may shorten history but not remove it.
		"below the floor": []byte(`{"target_id":"t-granted","retention_days":1}`),
	}
	for name, body := range cases {
		rec := doJSON(env.srv, http.MethodPut, path, "alice-tok", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", name, rec.Code)
		}
	}

	floor := int(spacecfg.MinRetentionWindow.Hours() / 24)
	body := fmt.Appendf(nil, `{"target_id":"t-granted","retention_days":%d}`, floor)
	if rec := doJSON(env.srv, http.MethodPut, path, "alice-tok", body); rec.Code != http.StatusOK {
		t.Fatalf("the floor itself must be accepted: status = %d, body %s", rec.Code, rec.Body)
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
		{http.MethodGet, "/api/v1/spaces/space-bob/backup/runs/0123abcd", nil},
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
		"/api/v1/spaces/space-alice/backup/runs/0123abcd",
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

// A member follows their own run by id. The restore folder travels with it,
// because it is what the UI links to when the run ends.
func TestGetRun_ReturnsTheSpacesRun(t *testing.T) {
	ctx := context.Background()
	env := newBackupTestEnv(t)

	j, err := env.jobs.Create(ctx, jobs.Job{
		SpaceID: "space-alice", Kind: jobs.KindRestore, State: jobs.StateRunning,
		SnapshotID: "snap-1", RestoreFolder: "Restore/2026-09-25T03-00-00Z",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := env.jobs.Finish(ctx, j.ID, jobs.Outcome{State: jobs.StateSucceeded, FileCount: 7, TotalBytes: 700}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	rec := doJSON(env.srv, http.MethodGet, "/api/v1/spaces/space-alice/backup/runs/"+j.ID, "alice-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var got jobResponse
	decodeBody(t, rec.Body, &got)
	if got.ID != j.ID || got.Kind != string(jobs.KindRestore) || got.State != string(jobs.StateSucceeded) {
		t.Fatalf("run = %+v", got)
	}
	if got.RestoreFolder != "Restore/2026-09-25T03-00-00Z" || got.FileCount != 7 || got.TotalBytes != 700 {
		t.Fatalf("run = %+v", got)
	}
}

// Another Space's job id, an id that never existed and an id that is not an id
// at all must produce the same answer, or the route enumerates other Spaces'
// runs for anyone who is a member of one Space.
func TestGetRun_UniformNotFound(t *testing.T) {
	ctx := context.Background()
	env := newBackupTestEnv(t)

	theirs, err := env.jobs.Create(ctx, jobs.Job{SpaceID: "space-bob", Kind: jobs.KindBackup, State: jobs.StateRunning})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var bodies []string
	for name, id := range map[string]string{
		"another space's run": theirs.ID,
		"never existed":       "0123456789abcdef0123456789abcdef",
		"not hex":             "job-1",
		"upper-case hex":      "ABCDEF",
		"too long":            strings.Repeat("a", maxJobIDLength+1),
	} {
		rec := doJSON(env.srv, http.MethodGet, "/api/v1/spaces/space-alice/backup/runs/"+id, "alice-tok", nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", name, rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Fatalf("404 bodies differ: %q vs %q", bodies[0], b)
		}
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
		{http.MethodGet, "/api/v1/spaces/space-alice/backup/runs/0123abcd"},
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

// --- PATCH /backup/config ---------------------------------------------------

const configPath = "/api/v1/spaces/space-alice/backup/config"

// seedConfig stores a complete configuration for alice's Space, so a patch has
// something to leave untouched.
func (e *backupTestEnv) seedConfig(t *testing.T) spacecfg.Config {
	t.Helper()
	cfg, err := e.configs.Put(context.Background(), spacecfg.Config{
		SpaceID: "space-alice", TargetID: "t-granted",
		RetentionWindow: 30 * 24 * time.Hour, Schedule: "0 4 * * 1", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	return cfg
}

func (e *backupTestEnv) patch(body string) *httptest.ResponseRecorder {
	return doJSON(e.srv, http.MethodPatch, configPath, "alice-tok", []byte(body))
}

func (e *backupTestEnv) stored(t *testing.T) spacecfg.Config {
	t.Helper()
	cfg, err := e.configs.Get(context.Background(), "space-alice")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return cfg
}

// The reason the route exists: changing one field leaves every other one
// exactly as stored, including the schedule PUT never sees.
func TestPatchBackupConfig_ChangesOnlyWhatIsSent(t *testing.T) {
	env := newBackupTestEnv(t)
	env.seedConfig(t)

	rec := env.patch(`{"retention_days":60}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var got configResponse
	decodeBody(t, rec.Body, &got)
	if got.RetentionDays != 60 {
		t.Fatalf("response = %+v", got)
	}
	stored := env.stored(t)
	if stored.RetentionWindow != 60*24*time.Hour || stored.TargetID != "t-granted" ||
		stored.Schedule != "0 4 * * 1" || !stored.Enabled {
		t.Fatalf("stored = %+v", stored)
	}

	if rec := env.patch(`{"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("patch enabled = %d %s", rec.Code, rec.Body)
	}
	stored = env.stored(t)
	if stored.Enabled || stored.RetentionWindow != 60*24*time.Hour {
		t.Fatalf("stored after enabled patch = %+v", stored)
	}
}

// Zero means "the default", exactly as it does for PUT.
func TestPatchBackupConfig_ZeroRetentionRestoresTheDefault(t *testing.T) {
	env := newBackupTestEnv(t)
	env.seedConfig(t)
	rec := env.patch(`{"retention_days":0}`)
	var got configResponse
	decodeBody(t, rec.Body, &got)
	if rec.Code != http.StatusOK || got.RetentionDays != int(spacecfg.DefaultRetentionWindow.Hours()/24) {
		t.Fatalf("status = %d, response = %+v", rec.Code, got)
	}
}

func TestPatchBackupConfig_EmptyPatchChangesNothing(t *testing.T) {
	env := newBackupTestEnv(t)
	before := env.seedConfig(t)
	if rec := env.patch(`{}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	after := env.stored(t)
	if after.TargetID != before.TargetID || after.RetentionWindow != before.RetentionWindow ||
		after.Schedule != before.Schedule || after.Enabled != before.Enabled {
		t.Fatalf("empty patch changed the record: %+v -> %+v", before, after)
	}
}

// A new target is held to the same grant check as PUT, with the same answer
// for "not granted" and "no such target".
func TestPatchBackupConfig_TargetIsGrantChecked(t *testing.T) {
	env := newBackupTestEnv(t)
	env.seedConfig(t)
	ctx := context.Background()

	for _, targetID := range []string{"t-secret", "t-does-not-exist"} {
		rec := env.patch(`{"target_id":"` + targetID + `"}`)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("target %q: status = %d, want 403", targetID, rec.Code)
		}
	}
	if got := env.stored(t).TargetID; got != "t-granted" {
		t.Fatalf("refused patch changed the target to %q", got)
	}

	if _, err := env.targets.CreateTarget(ctx, targets.Target{ID: "t-second", Name: "Second"}); err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	if err := env.targets.PutGrant(ctx, targets.Grant{TargetID: "t-second", Scope: targets.ScopeUser, UserSub: "alice"}); err != nil {
		t.Fatalf("PutGrant: %v", err)
	}
	if rec := env.patch(`{"target_id":"t-second"}`); rec.Code != http.StatusOK {
		t.Fatalf("granted target: status = %d %s", rec.Code, rec.Body)
	}
	if got := env.stored(t); got.TargetID != "t-second" || got.RetentionWindow != 30*24*time.Hour {
		t.Fatalf("stored = %+v", got)
	}
}

func TestPatchBackupConfig_Validation(t *testing.T) {
	cases := map[string]string{
		"malformed body":         `{`,
		"retention below floor":  `{"retention_days":3}`,
		"negative retention":     `{"retention_days":-1}`,
		"empty target":           `{"target_id":""}`,
		"unknown field":          `{"schedule":"* * * * *"}`,
		"wrong type":             `{"enabled":"yes"}`,
		"unknown beside a known": `{"retention_days":30,"retention":30}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			env := newBackupTestEnv(t)
			before := env.seedConfig(t)
			rec := env.patch(body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body)
			}
			if after := env.stored(t); after.RetentionWindow != before.RetentionWindow || after.TargetID != before.TargetID {
				t.Fatalf("refused patch changed the record: %+v", after)
			}
		})
	}
}

// Binding a target is PUT's job; PATCH does not create a configuration.
func TestPatchBackupConfig_UnconfiguredSpaceIsNotFound(t *testing.T) {
	env := newBackupTestEnv(t)
	rec := env.patch(`{"target_id":"t-granted"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if _, err := env.configs.Get(context.Background(), "space-alice"); err == nil {
		t.Fatal("PATCH created a configuration")
	}
}

func TestPatchBackupConfig_IsMemberGated(t *testing.T) {
	env := newBackupTestEnv(t)
	env.seedConfig(t)
	if rec := doJSON(env.srv, http.MethodPatch, configPath, "bob-tok", []byte(`{"enabled":false}`)); rec.Code != http.StatusForbidden {
		t.Fatalf("non-member = %d, want 403", rec.Code)
	}
	if rec := doJSON(env.srv, http.MethodPatch, configPath, "", []byte(`{"enabled":false}`)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", rec.Code)
	}
	if !env.stored(t).Enabled {
		t.Fatal("refused patch took effect")
	}
}
