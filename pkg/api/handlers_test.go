package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/targets"
)

// fakeSpaceReader is a cs3.SpaceReader stub for handler tests.
type fakeSpaceReader struct {
	spaces []cs3.Space
	err    error
}

func (f fakeSpaceReader) ListSpaces(context.Context) ([]cs3.Space, error) {
	return f.spaces, f.err
}
func (f fakeSpaceReader) ListDir(context.Context, cs3.Space, string) ([]cs3.Entry, error) {
	return nil, nil
}
func (f fakeSpaceReader) Walk(context.Context, cs3.Space, func(cs3.Entry) error) error { return nil }
func (f fakeSpaceReader) OpenFile(context.Context, cs3.Space, string, int64) (io.ReadCloser, error) {
	return nil, errors.New("not used")
}

// grants builds a Space's member map from principal -> role, for tests that do
// not care about expiry or group grants.
func grants(roles map[string]cs3.Role) map[string]cs3.Member {
	m := make(map[string]cs3.Member, len(roles))
	for principal, role := range roles {
		m[principal] = cs3.Member{Role: role}
	}
	return m
}

func decodeBody(t *testing.T, r io.Reader, v any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func authGet(srv *Server, path, token string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHealthzAndReadyz(t *testing.T) {
	srv := NewServer()
	if rec := authGet(srv, "/healthz", ""); rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
	}
	if rec := authGet(srv, "/readyz", ""); rec.Code != http.StatusOK {
		t.Fatalf("readyz (default ready) = %d", rec.Code)
	}

	failing := NewServer(WithReadiness(func(context.Context) error { return errors.New("down") }))
	if rec := authGet(failing, "/readyz", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz (failing) = %d, want 503", rec.Code)
	}
}

func TestListSpaces_MembershipEnforced(t *testing.T) {
	val := fakeValidator{tokens: map[string]string{
		"alice-tok": "alice",
		"bob-tok":   "bob",
	}}
	reader := fakeSpaceReader{spaces: []cs3.Space{
		{ID: "personal-alice", Name: "Alice", Type: "personal", Owner: "alice"},
		{ID: "project-x", Name: "Project X", Type: "project", Members: grants(map[string]cs3.Role{"alice": cs3.RoleManager})},
		{ID: "personal-bob", Name: "Bob", Type: "personal", Owner: "bob"},
	}}
	srv := NewServer(WithTokenValidator(val), WithSpaceReader(reader))

	// Alice sees her personal space + the project she is a member of, not Bob's.
	rec := authGet(srv, "/api/v1/spaces", "alice-tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Spaces []spaceDTO `json:"spaces"`
	}
	decodeBody(t, rec.Body, &got)
	ids := map[string]bool{}
	for _, s := range got.Spaces {
		ids[s.ID] = true
	}
	if !ids["personal-alice"] || !ids["project-x"] {
		t.Fatalf("alice should see her spaces: %+v", got.Spaces)
	}
	if ids["personal-bob"] {
		t.Fatalf("alice must not see bob's space (foreign space): %+v", got.Spaces)
	}
}

func TestListSpaces_ForeignUserSeesNothing(t *testing.T) {
	val := fakeValidator{tokens: map[string]string{"carol-tok": "carol"}}
	reader := fakeSpaceReader{spaces: []cs3.Space{
		{ID: "personal-alice", Name: "Alice", Type: "personal", Owner: "alice"},
	}}
	srv := NewServer(WithTokenValidator(val), WithSpaceReader(reader))

	rec := authGet(srv, "/api/v1/spaces", "carol-tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got struct {
		Spaces []spaceDTO `json:"spaces"`
	}
	decodeBody(t, rec.Body, &got)
	if len(got.Spaces) != 0 {
		t.Fatalf("foreign user must see no spaces: %+v", got.Spaces)
	}
}

func TestListSpaces_UpstreamErrorMasked(t *testing.T) {
	val := fakeValidator{tokens: map[string]string{"tok": "u"}}
	reader := fakeSpaceReader{err: errors.New("cs3 gateway 500: internal detail leak")}
	srv := NewServer(WithTokenValidator(val), WithSpaceReader(reader))

	rec := authGet(srv, "/api/v1/spaces", "tok")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if body := rec.Body.String(); contains(body, "internal detail leak") || contains(body, "cs3") {
		t.Fatalf("upstream detail leaked to client: %s", body)
	}
}

func TestListTargets_GrantedOnlyAndLeastDisclosure(t *testing.T) {
	val := fakeValidator{tokens: map[string]string{
		"alice-tok": "alice",
		"bob-tok":   "bob",
	}}
	reader := fakeSpaceReader{spaces: []cs3.Space{
		{ID: "space-shared", Name: "Shared", Type: "project", Members: grants(map[string]cs3.Role{"alice": cs3.RoleManager})},
	}}
	store := targets.NewMemoryStore()
	ctx := context.Background()
	_, _ = store.CreateTarget(ctx, targets.Target{ID: "t-all", Name: "AllUsers", Endpoint: "http://secret", Bucket: "b", WrappedCreds: []byte("sealed")})
	_, _ = store.CreateTarget(ctx, targets.Target{ID: "t-alice", Name: "AliceOnly", WrappedCreds: []byte("sealed")})
	_, _ = store.CreateTarget(ctx, targets.Target{ID: "t-space", Name: "SharedSpace", WrappedCreds: []byte("sealed")})
	_ = store.PutGrant(ctx, targets.Grant{TargetID: "t-all", Scope: targets.ScopeAllUsers})
	_ = store.PutGrant(ctx, targets.Grant{TargetID: "t-alice", Scope: targets.ScopeUser, UserSub: "alice"})
	_ = store.PutGrant(ctx, targets.Grant{TargetID: "t-space", Scope: targets.ScopeSpace, SpaceID: "space-shared"})

	srv := NewServer(WithTokenValidator(val), WithSpaceReader(reader), WithAuthorizer(store))

	// Alice: all-users + her user grant + the shared-space grant.
	rec := authGet(srv, "/api/v1/targets", "alice-tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	// Response must never carry credentials/endpoint/bucket.
	if body := rec.Body.String(); contains(body, "sealed") || contains(body, "secret") || contains(body, "endpoint") || contains(body, "bucket") {
		t.Fatalf("least-disclosure violated, sensitive field in response: %s", body)
	}
	var got struct {
		Targets []targetDTO `json:"targets"`
	}
	decodeBody(t, rec.Body, &got)
	ids := map[string]bool{}
	for _, tt := range got.Targets {
		ids[tt.ID] = true
	}
	if !ids["t-all"] || !ids["t-alice"] || !ids["t-space"] {
		t.Fatalf("alice should see all three granted targets: %+v", got.Targets)
	}

	// Bob: only the all-users target.
	rec = authGet(srv, "/api/v1/targets", "bob-tok")
	var gotBob struct {
		Targets []targetDTO `json:"targets"`
	}
	decodeBody(t, rec.Body, &gotBob)
	if len(gotBob.Targets) != 1 || gotBob.Targets[0].ID != "t-all" {
		t.Fatalf("bob should see only the all-users target: %+v", gotBob.Targets)
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
