package api

// Restore endpoints (Path B). The authorization negatives are the important
// part: a Space's snapshots and its restore trigger are member-only, and being
// an OpenCloud admin buys nothing (decisions.md #2, #15).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/restore"
	"opencloud-backup-plugin/pkg/snapshot"
)

// errUnexpected stands in for an unclassified internal failure.
var errUnexpected = errors.New("s3: connection reset by peer at buddy.example:3900")

// fakeRestorer records calls and returns canned answers.
type fakeRestorer struct {
	snapshots []snapshot.Info
	jobID     string
	listErr   error
	startErr  error

	listed  []string
	started []startedRestore
}

type startedRestore struct {
	spaceID    string
	snapshotID snapshot.SnapshotID
}

func (f *fakeRestorer) ListSnapshots(_ context.Context, spaceID string) ([]snapshot.Info, error) {
	f.listed = append(f.listed, spaceID)
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.snapshots, nil
}

func (f *fakeRestorer) StartRestore(_ context.Context, spaceID string, id snapshot.SnapshotID) (string, error) {
	f.started = append(f.started, startedRestore{spaceID: spaceID, snapshotID: id})
	if f.startErr != nil {
		return "", f.startErr
	}
	return f.jobID, nil
}

type restoreTestEnv struct {
	srv      *Server
	restorer *fakeRestorer
}

// newRestoreTestEnv wires a server where alice owns a space, bob owns another,
// and "root" is an OpenCloud admin who is a member of neither.
func newRestoreTestEnv(t *testing.T) *restoreTestEnv {
	t.Helper()

	val := fakeValidator{tokens: map[string]string{
		"alice-tok": "alice",
		"bob-tok":   "bob",
		"root-tok":  "root",
	}}
	reader := fakeSpaceReader{spaces: []cs3.Space{
		{ID: "space-alice", Name: "Alice", Type: "personal", Owner: "alice"},
		{ID: "space-bob", Name: "Bob", Type: "personal", Owner: "bob"},
	}}

	env := &restoreTestEnv{restorer: &fakeRestorer{
		jobID: "job-restore-1",
		snapshots: []snapshot.Info{
			{
				ID:         "snap-2",
				StartTime:  time.Date(2026, 7, 8, 9, 10, 11, 0, time.UTC),
				FileCount:  12,
				TotalBytes: 3456,
			},
			{
				ID:         "snap-1",
				StartTime:  time.Date(2026, 7, 1, 9, 10, 11, 0, time.UTC),
				FileCount:  10,
				TotalBytes: 3000,
			},
		},
	}}
	env.srv = NewServer(
		WithTokenValidator(val),
		WithSpaceReader(reader),
		WithAdminResolver(NewAllowlistAdminResolver([]string{"root"})),
		WithRestoreRunner(env.restorer),
	)
	return env
}

func TestListSnapshots_ServesMembers(t *testing.T) {
	env := newRestoreTestEnv(t)

	rec := authGet(env.srv, "/api/v1/spaces/space-alice/snapshots", "alice-tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	var got struct {
		Snapshots []snapshotDTO `json:"snapshots"`
	}
	decodeBody(t, rec.Body, &got)
	if len(got.Snapshots) != 2 {
		t.Fatalf("snapshots = %+v", got.Snapshots)
	}
	if got.Snapshots[0].ID != "snap-2" || got.Snapshots[0].FileCount != 12 || got.Snapshots[0].TotalBytes != 3456 {
		t.Fatalf("first snapshot = %+v", got.Snapshots[0])
	}
	if got.Snapshots[0].TakenAt != "2026-07-08T09:10:11Z" {
		t.Fatalf("taken_at = %q", got.Snapshots[0].TakenAt)
	}
}

func TestRestore_StartsForMember(t *testing.T) {
	env := newRestoreTestEnv(t)

	rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/restore", "alice-tok",
		[]byte(`{"snapshot_id":"snap-2"}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	var got restoreResponse
	decodeBody(t, rec.Body, &got)
	if got.JobID != "job-restore-1" || got.SpaceID != "space-alice" || got.SnapshotID != "snap-2" {
		t.Fatalf("response = %+v", got)
	}
	if len(env.restorer.started) != 1 || env.restorer.started[0].snapshotID != "snap-2" {
		t.Fatalf("runner calls = %+v", env.restorer.started)
	}
}

// A non-member may neither see a Space's snapshots nor restore into it, and
// learns nothing about whether the Space exists.
func TestRestore_NonMemberIsRefused(t *testing.T) {
	env := newRestoreTestEnv(t)

	rec := authGet(env.srv, "/api/v1/spaces/space-alice/snapshots", "bob-tok")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("snapshots status = %d, want 403", rec.Code)
	}
	rec = doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/restore", "bob-tok",
		[]byte(`{"snapshot_id":"snap-2"}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("restore status = %d, want 403", rec.Code)
	}

	// The same answer for a space that does not exist at all: no enumeration.
	rec = authGet(env.srv, "/api/v1/spaces/space-nonexistent/snapshots", "bob-tok")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unknown space status = %d, want 403", rec.Code)
	}

	if len(env.restorer.started) != 0 || len(env.restorer.listed) != 0 {
		t.Fatal("a refused request must never reach the runner")
	}
}

// Decision #2/#15: there is no admin path into a user's Space. An admin who is
// not a member is refused exactly like any stranger.
func TestRestore_AdminWhoIsNotAMemberIsRefused(t *testing.T) {
	env := newRestoreTestEnv(t)

	rec := authGet(env.srv, "/api/v1/spaces/space-alice/snapshots", "root-tok")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin snapshots status = %d, want 403", rec.Code)
	}
	rec = doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/restore", "root-tok",
		[]byte(`{"snapshot_id":"snap-2"}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin restore status = %d, want 403", rec.Code)
	}
	if len(env.restorer.started) != 0 {
		t.Fatal("an admin must not be able to trigger a restore into a user's space")
	}
}

func TestRestore_RequiresAuthentication(t *testing.T) {
	env := newRestoreTestEnv(t)

	if rec := authGet(env.srv, "/api/v1/spaces/space-alice/snapshots", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/restore", "",
		[]byte(`{"snapshot_id":"snap-2"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRestore_Validation(t *testing.T) {
	env := newRestoreTestEnv(t)
	path := "/api/v1/spaces/space-alice/restore"

	for name, body := range map[string][]byte{
		"malformed body":   []byte(`{`),
		"missing snapshot": []byte(`{}`),
		"empty snapshot":   []byte(`{"snapshot_id":""}`),
	} {
		rec := doJSON(env.srv, http.MethodPost, path, "alice-tok", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}
}

func TestRestore_RunnerErrorsMapToStatusCodes(t *testing.T) {
	cases := []struct {
		err  error
		want int
		code string
	}{
		{restore.ErrSnapshotNotFound, http.StatusNotFound, "not_found"},
		{restore.ErrSpaceNotFound, http.StatusNotFound, "not_found"},
		{restore.ErrNotConfigured, http.StatusConflict, "not_configured"},
		{restore.ErrTargetUnavailable, http.StatusConflict, "target_unavailable"},
		{restore.ErrRunInProgress, http.StatusConflict, "run_in_progress"},
		{errUnexpected, http.StatusInternalServerError, "internal_error"},
	}
	for _, tc := range cases {
		env := newRestoreTestEnv(t)
		env.restorer.startErr = tc.err

		rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/restore", "alice-tok",
			[]byte(`{"snapshot_id":"snap-2"}`))
		if rec.Code != tc.want {
			t.Fatalf("%v: status = %d, want %d", tc.err, rec.Code, tc.want)
		}

		var body errorBody
		decodeBody(t, rec.Body, &body)
		if body.Error.Code != tc.code {
			t.Fatalf("%v: error code = %q, want %q", tc.err, body.Error.Code, tc.code)
		}
		// Internal detail must never reach the client.
		if body.Error.Message == tc.err.Error() && tc.err == errUnexpected {
			t.Fatalf("internal error text leaked: %q", body.Error.Message)
		}
	}
}

// Without a restore worker wired, the routes fail closed rather than 404.
func TestRestore_UnavailableWithoutRunner(t *testing.T) {
	srv := NewServer(
		WithTokenValidator(fakeValidator{tokens: map[string]string{"alice-tok": "alice"}}),
		WithSpaceReader(fakeSpaceReader{spaces: []cs3.Space{
			{ID: "space-alice", Owner: "alice"},
		}}),
	)

	if rec := authGet(srv, "/api/v1/spaces/space-alice/snapshots", "alice-tok"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	rec := doJSON(srv, http.MethodPost, "/api/v1/spaces/space-alice/restore", "alice-tok",
		[]byte(`{"snapshot_id":"snap-2"}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// The client cannot choose where a restore lands: the request carries only a
// snapshot id, and any extra field is ignored rather than honoured.
func TestRestore_ClientCannotChooseDestination(t *testing.T) {
	env := newRestoreTestEnv(t)

	rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/restore", "alice-tok",
		[]byte(`{"snapshot_id":"snap-2","target_path":"/","overwrite":true}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	var req restoreRequest
	if err := json.Unmarshal([]byte(`{"snapshot_id":"snap-2","target_path":"/"}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// restoreRequest has exactly one field; nothing else can be expressed.
	if req.SnapshotID != "snap-2" {
		t.Fatalf("request = %+v", req)
	}
}
