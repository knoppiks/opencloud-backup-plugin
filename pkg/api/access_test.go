package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/notify"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/state"
	"opencloud-backup-plugin/pkg/targets"
)

// --- fakes ------------------------------------------------------------------

// countingSpaceReader counts ListSpaces calls so a test can prove the per-request
// cache is doing its job.
type countingSpaceReader struct {
	spaces []cs3.Space
	calls  int
}

func (c *countingSpaceReader) ListSpaces(context.Context) ([]cs3.Space, error) {
	c.calls++
	return c.spaces, nil
}
func (c *countingSpaceReader) ListDir(context.Context, cs3.Space, string) ([]cs3.Entry, error) {
	return nil, nil
}
func (c *countingSpaceReader) Walk(context.Context, cs3.Space, func(cs3.Entry) error) error {
	return nil
}
func (c *countingSpaceReader) OpenFile(context.Context, cs3.Space, string, int64) (io.ReadCloser, error) {
	return nil, errors.New("not used")
}

// countingGroupResolver returns fixed groups per subject and counts lookups.
type countingGroupResolver struct {
	bySubject map[string][]string
	err       error
	calls     int
}

func (c *countingGroupResolver) Groups(_ context.Context, id Identity) ([]string, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return c.bySubject[id.Subject], nil
}

// --- environment ------------------------------------------------------------

// roleTestEnv wires one server over a single shared Space carrying every kind of
// grant the role model reads, plus enough downstream fakes that each route can
// reach its handler once authorization allows it.
type roleTestEnv struct {
	srv    *Server
	reader *countingSpaceReader
	groups *countingGroupResolver
	now    time.Time
}

const (
	roleSpace     = "space-team"
	roleGroupID   = "group-family"
	roleTargetID  = "t-granted"
	roleSnapshot  = "snap-1"
	roleTestToken = "tok-"
)

// roleTestSubjects maps each test caller to the authority they hold on
// roleSpace. Tokens are "tok-<subject>".
var roleTestSubjects = []string{
	"viewer", "editor", "manager", "lapsed", "grouped", "stranger",
}

func newRoleTestEnv(t *testing.T, opts ...Option) *roleTestEnv {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	tokens := map[string]string{}
	for _, sub := range roleTestSubjects {
		tokens[roleTestToken+sub] = sub
	}

	reader := &countingSpaceReader{spaces: []cs3.Space{{
		ID: roleSpace, Name: "Team", Type: "project",
		Members: map[string]cs3.Member{
			"viewer":    {Role: cs3.RoleViewer},
			"editor":    {Role: cs3.RoleEditor},
			"manager":   {Role: cs3.RoleManager},
			"lapsed":    {Role: cs3.RoleManager, ExpiresAt: now.Add(-time.Hour)},
			roleGroupID: {Role: cs3.RoleEditor, Group: true},
		},
	}}}
	groups := &countingGroupResolver{bySubject: map[string][]string{
		"grouped": {roleGroupID},
	}}

	targetStore := targets.NewMemoryStore()
	if _, err := targetStore.CreateTarget(ctx, targets.Target{ID: roleTargetID, Name: "Buddy S3"}); err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	if err := targetStore.PutGrant(ctx, targets.Grant{
		TargetID: roleTargetID, Scope: targets.ScopeAllUsers,
	}); err != nil {
		t.Fatalf("PutGrant: %v", err)
	}

	configs := spacecfg.NewMemoryStore()
	if _, err := configs.Put(ctx, spacecfg.Config{
		SpaceID: roleSpace, TargetID: roleTargetID,
		RetentionWindow: 30 * 24 * time.Hour, Enabled: true,
	}); err != nil {
		t.Fatalf("Put config: %v", err)
	}

	keyStore := keys.NewMemoryStore()
	srwKey, err := keys.GenerateSRWKey()
	if err != nil {
		t.Fatalf("GenerateSRWKey: %v", err)
	}
	wrapper, err := keys.NewSRWWrapper(srwKey)
	if err != nil {
		t.Fatalf("NewSRWWrapper: %v", err)
	}

	base := []Option{
		WithTokenValidator(fakeValidator{tokens: tokens}),
		WithSpaceReader(reader),
		WithGroupResolver(groups),
		WithAuthorizer(targetStore),
		WithSpaceConfigStore(configs),
		WithKeyStore(keyStore),
		WithSRWWrapper(wrapper),
		WithJobStore(jobs.NewMemoryStore()),
		WithBackupRunner(&fakeRunner{jobID: "job-1"}),
		WithRestoreRunner(&fakeRestorer{jobID: "job-2", snapshots: []snapshot.Info{{ID: roleSnapshot}}}),
		WithNotificationStore(notify.NewStateStore(state.NewMemoryStore(), nil)),
		WithClock(func() time.Time { return now }),
	}
	return &roleTestEnv{
		srv:    NewServer(append(base, opts...)...),
		reader: reader,
		groups: groups,
		now:    now,
	}
}

// call issues a request to a roleSpace route as the given subject.
func (e *roleTestEnv) call(method, path, subject string, body []byte) *httptest.ResponseRecorder {
	return doJSON(e.srv, method, "/api/v1/spaces/"+roleSpace+path, roleTestToken+subject, body)
}

// --- the role table ---------------------------------------------------------

// route is one entry of the role table under test.
type route struct {
	name    string
	method  string
	path    string
	body    []byte
	minRole cs3.Role
}

// roleTable is the authorization contract of every space-scoped route. It is
// the table in access.go's package comment, expressed as a test: adding a route
// without a row here is the mistake this guards against.
func roleTable() []route {
	return []route{
		{"get config", http.MethodGet, "/backup/config", nil, cs3.RoleViewer},
		{"list runs", http.MethodGet, "/backup/runs", nil, cs3.RoleViewer},
		{"backup status", http.MethodGet, "/backup/status", nil, cs3.RoleViewer},
		{"get schedule", http.MethodGet, "/backup/schedule", nil, cs3.RoleViewer},
		{"notifications", http.MethodGet, "/backup/notifications", nil, cs3.RoleViewer},
		{"key status", http.MethodGet, "/backup/keystatus", nil, cs3.RoleViewer},
		{"recovery envelope", http.MethodGet, "/backup/recovery-envelope", nil, cs3.RoleViewer},
		{"list snapshots", http.MethodGet, "/snapshots", nil, cs3.RoleViewer},
		{"restore", http.MethodPost, "/restore", []byte(`{"snapshot_id":"` + roleSnapshot + `"}`), cs3.RoleViewer},

		{"put config", http.MethodPut, "/backup/config",
			[]byte(`{"target_id":"` + roleTargetID + `","retention_days":30,"enabled":true}`), cs3.RoleEditor},
		{"put schedule", http.MethodPut, "/backup/schedule",
			[]byte(`{"schedule":"0 3 * * *"}`), cs3.RoleEditor},
		{"run backup", http.MethodPost, "/backup/run", nil, cs3.RoleEditor},

		{"key setup", http.MethodPost, "/backup/setup", []byte(`{}`), cs3.RoleManager},
		{"rotate recovery key", http.MethodPost, "/backup/recovery-key/rotate", []byte(`{}`), cs3.RoleManager},
	}
}

// subjectRoles is what each test caller holds on roleSpace directly.
var subjectRoles = map[string]cs3.Role{
	"viewer":   cs3.RoleViewer,
	"editor":   cs3.RoleEditor,
	"manager":  cs3.RoleManager,
	"lapsed":   cs3.RoleNone, // grant expired
	"stranger": cs3.RoleNone,
}

// TestRoleTable_EveryRouteEnforcesItsMinimumRole walks the full cross-product of
// callers and routes. A caller below the route's minimum must get 403; a caller
// at or above it must get past authorization (anything but 403/401).
func TestRoleTable_EveryRouteEnforcesItsMinimumRole(t *testing.T) {
	for _, r := range roleTable() {
		for subject, held := range subjectRoles {
			t.Run(r.name+"/"+subject, func(t *testing.T) {
				env := newRoleTestEnv(t)
				rec := env.call(r.method, r.path, subject, r.body)

				if held < r.minRole {
					if rec.Code != http.StatusForbidden {
						t.Fatalf("%s as %s = %d, want 403 (needs %v, holds %v)",
							r.name, subject, rec.Code, r.minRole, held)
					}
					return
				}
				if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
					t.Fatalf("%s as %s = %d, want to pass authorization (holds %v, needs %v): %s",
						r.name, subject, rec.Code, held, r.minRole, rec.Body.String())
				}
			})
		}
	}
}

// TestExpiredGrantIsNotAGrant isolates the expiry dimension: the same caller is
// admitted before their grant lapses and refused after, with nothing else
// changing.
func TestExpiredGrantIsNotAGrant(t *testing.T) {
	before := newRoleTestEnv(t, WithClock(func() time.Time {
		return time.Unix(1_700_000_000, 0).UTC().Add(-2 * time.Hour)
	}))
	if rec := before.call(http.MethodGet, "/backup/status", "lapsed", nil); rec.Code == http.StatusForbidden {
		t.Fatalf("before expiry = 403, want admitted: %s", rec.Body.String())
	}

	after := newRoleTestEnv(t)
	if rec := after.call(http.MethodGet, "/backup/status", "lapsed", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("after expiry = %d, want 403", rec.Code)
	}
}

// TestExpiredMemberIsNotListedASpace covers the listing path, which has its own
// filter: an expired grant must not keep a Space visible.
func TestExpiredMemberDoesNotSeeTheSpace(t *testing.T) {
	env := newRoleTestEnv(t)
	rec := authGet(env.srv, "/api/v1/spaces", roleTestToken+"lapsed")
	if rec.Code != http.StatusOK {
		t.Fatalf("list spaces = %d", rec.Code)
	}
	var got struct {
		Spaces []spaceDTO `json:"spaces"`
	}
	decodeBody(t, rec.Body, &got)
	if len(got.Spaces) != 0 {
		t.Fatalf("expired member must see no spaces: %+v", got.Spaces)
	}
}

// --- group grants -----------------------------------------------------------

// TestGroupGrantIsHonoured is the point of resolving groups at all: a caller
// with no direct grant reaches the Space through their group.
func TestGroupGrantIsHonoured(t *testing.T) {
	env := newRoleTestEnv(t)

	// The group grant is an editor grant, so it carries both role levels.
	if rec := env.call(http.MethodGet, "/backup/status", "grouped", nil); rec.Code == http.StatusForbidden {
		t.Fatalf("group member refused a viewer route: %s", rec.Body.String())
	}
	if rec := env.call(http.MethodPost, "/backup/run", "grouped", nil); rec.Code == http.StatusForbidden {
		t.Fatalf("group member refused an editor route: %s", rec.Body.String())
	}
	// ...but not the manager routes.
	if rec := env.call(http.MethodPost, "/backup/setup", "grouped", []byte(`{}`)); rec.Code != http.StatusForbidden {
		t.Fatalf("group editor reached a manager route = %d", rec.Code)
	}
	// And the Space is listed for them.
	rec := authGet(env.srv, "/api/v1/spaces", roleTestToken+"grouped")
	var got struct {
		Spaces []spaceDTO `json:"spaces"`
	}
	decodeBody(t, rec.Body, &got)
	if len(got.Spaces) != 1 || got.Spaces[0].ID != roleSpace {
		t.Fatalf("group member should see the space: %+v", got.Spaces)
	}
}

// TestGroupsAreResolvedOnlyWhenTheDirectGrantFallsShort pins the cost model: a
// caller whose own grant already suffices must not trigger an upstream lookup.
func TestGroupsAreResolvedOnlyWhenNeeded(t *testing.T) {
	env := newRoleTestEnv(t)
	if rec := env.call(http.MethodGet, "/backup/status", "manager", nil); rec.Code == http.StatusForbidden {
		t.Fatalf("manager refused: %s", rec.Body.String())
	}
	if env.groups.calls != 0 {
		t.Fatalf("resolved groups %d time(s) for a caller with a sufficient direct grant", env.groups.calls)
	}

	// An insufficient direct grant does consult them — once.
	env = newRoleTestEnv(t)
	if rec := env.call(http.MethodPost, "/backup/run", "viewer", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer reached an editor route = %d", rec.Code)
	}
	if env.groups.calls != 1 {
		t.Fatalf("group lookups = %d, want exactly 1", env.groups.calls)
	}
}

// TestGroupResolutionFailsClosed: when groups cannot be resolved, a Space with a
// group grant must refuse rather than guess. Both directions of the guess are
// wrong, so the request is answered with an error instead of a decision.
func TestGroupResolutionFailsClosed(t *testing.T) {
	t.Run("no resolver configured", func(t *testing.T) {
		env := newRoleTestEnv(t, WithGroupResolver(nil))
		rec := env.call(http.MethodGet, "/backup/status", "grouped", nil)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503, body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("resolver fails", func(t *testing.T) {
		env := newRoleTestEnv(t)
		env.groups.err = errors.New("graph 500: internal detail leak")
		rec := env.call(http.MethodGet, "/backup/status", "grouped", nil)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", rec.Code)
		}
		if contains(rec.Body.String(), "internal detail leak") {
			t.Fatalf("upstream detail leaked: %s", rec.Body.String())
		}
	})
}

// A caller whose own grant already suffices must not be held hostage by a
// broken group resolver.
func TestDirectGrantSurvivesABrokenGroupResolver(t *testing.T) {
	env := newRoleTestEnv(t)
	env.groups.err = errors.New("graph down")
	if rec := env.call(http.MethodGet, "/backup/status", "viewer", nil); rec.Code == http.StatusForbidden ||
		rec.Code == http.StatusBadGateway || rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("direct viewer blocked by group resolver failure: %d %s", rec.Code, rec.Body.String())
	}
}

// --- per-request caching ----------------------------------------------------

// TestSpaceListIsFetchedOncePerRequest guards R3 task 5. GET /targets used to
// list spaces twice; the per-request cache makes every handler pay once.
func TestSpaceListIsFetchedOncePerRequest(t *testing.T) {
	env := newRoleTestEnv(t)

	if rec := env.call(http.MethodPut, "/backup/config", "editor",
		[]byte(`{"target_id":"`+roleTargetID+`","retention_days":30,"enabled":true}`)); rec.Code != http.StatusOK {
		t.Fatalf("put config = %d body=%s", rec.Code, rec.Body.String())
	}
	if env.reader.calls != 1 {
		t.Fatalf("ListSpaces called %d times in one request, want 1", env.reader.calls)
	}

	env.reader.calls = 0
	if rec := authGet(env.srv, "/api/v1/targets", roleTestToken+"editor"); rec.Code != http.StatusOK {
		t.Fatalf("list targets = %d", rec.Code)
	}
	if env.reader.calls != 1 {
		t.Fatalf("ListSpaces called %d times for GET /targets, want 1", env.reader.calls)
	}
}

// TestGroupsAreResolvedOncePerRequest: the listing path evaluates every Space,
// so an unmemoised lookup would fan out.
func TestGroupsAreResolvedOncePerRequest(t *testing.T) {
	env := newRoleTestEnv(t)
	env.reader.spaces = append(env.reader.spaces, cs3.Space{
		ID: "space-other", Name: "Other", Type: "project",
		Members: map[string]cs3.Member{roleGroupID: {Role: cs3.RoleViewer, Group: true}},
	})

	if rec := authGet(env.srv, "/api/v1/spaces", roleTestToken+"grouped"); rec.Code != http.StatusOK {
		t.Fatalf("list spaces = %d", rec.Code)
	}
	if env.groups.calls != 1 {
		t.Fatalf("group lookups = %d across a multi-space listing, want 1", env.groups.calls)
	}
}

// --- graph group resolver ---------------------------------------------------

func TestGraphGroupResolver(t *testing.T) {
	var gotAuth, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		if r.URL.Path != "/graph/v1.0/me" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "u1",
			"memberOf": []map[string]string{
				{"id": "g1", "displayName": "family"},
				{"id": ""},
				{"id": "g2"},
			},
		})
	}))
	defer srv.Close()

	r := NewGraphGroupResolver(srv.URL, srv.Client())
	got, err := r.Groups(context.Background(), Identity{Subject: "u1", Token: "caller-token"})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(got) != 2 || got[0] != "g1" || got[1] != "g2" {
		t.Fatalf("groups = %v, want [g1 g2] (empty ids dropped)", got)
	}
	if gotAuth != "Bearer caller-token" {
		t.Fatalf("caller token not forwarded: %q", gotAuth)
	}
	if !contains(gotQuery, "memberOf") {
		t.Fatalf("memberOf not expanded: %q", gotQuery)
	}
}

func TestGraphGroupResolver_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	r := NewGraphGroupResolver(srv.URL, srv.Client())
	if _, err := r.Groups(context.Background(), Identity{Subject: "u1", Token: "t"}); err == nil {
		t.Fatal("non-200 must error rather than report no groups")
	}
	if _, err := r.Groups(context.Background(), Identity{Subject: "u1"}); err == nil {
		t.Fatal("a caller with no token must error rather than report no groups")
	}
}

func TestGroupResolverFunc(t *testing.T) {
	var f GroupResolver = GroupResolverFunc(func(context.Context, Identity) ([]string, error) {
		return []string{"g"}, nil
	})
	got, err := f.Groups(context.Background(), Identity{})
	if err != nil || len(got) != 1 || got[0] != "g" {
		t.Fatalf("GroupResolverFunc: got %v err %v", got, err)
	}
}

// --- denial shape -----------------------------------------------------------

// TestDenialsDoNotDistinguishAbsentFromForbidden: an unknown Space and a
// forbidden one must be indistinguishable, or membership becomes enumerable.
func TestDenialsDoNotDistinguishAbsentFromForbidden(t *testing.T) {
	env := newRoleTestEnv(t)
	forbidden := env.call(http.MethodGet, "/backup/status", "stranger", nil)
	absent := doJSON(env.srv, http.MethodGet, "/api/v1/spaces/no-such-space/backup/status",
		roleTestToken+"viewer", nil)

	if forbidden.Code != absent.Code {
		t.Fatalf("status codes differ: forbidden=%d absent=%d", forbidden.Code, absent.Code)
	}
	if forbidden.Body.String() != absent.Body.String() {
		t.Fatalf("bodies differ:\n forbidden=%s absent=%s", forbidden.Body.String(), absent.Body.String())
	}
}

// A denial must never name the Space's members or their roles.
func TestDenialDoesNotDiscloseMembership(t *testing.T) {
	env := newRoleTestEnv(t)
	body := env.call(http.MethodPost, "/backup/setup", "viewer", []byte(`{}`)).Body.String()
	for _, secret := range []string{"manager\"", "editor\"", roleGroupID, "lapsed"} {
		if contains(body, secret) {
			t.Fatalf("denial disclosed membership detail %q: %s", secret, body)
		}
	}
}

// --- unauthenticated --------------------------------------------------------

func TestRoleGatedRoutesRequireAuthentication(t *testing.T) {
	env := newRoleTestEnv(t)
	for _, r := range roleTable() {
		rec := doJSON(env.srv, r.method, "/api/v1/spaces/"+roleSpace+r.path, "", r.body)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated = %d, want 401", r.name, rec.Code)
		}
	}
}
