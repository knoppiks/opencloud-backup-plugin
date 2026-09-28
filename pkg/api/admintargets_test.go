package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/objstore"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/targets"
)

// --- environment ------------------------------------------------------------

const (
	adminToken = "admin-tok"
	userToken  = "user-tok"

	// Credential markers for the admin tests. Nothing the API returns may
	// contain either, in any encoding.
	adminAccessKeyID       = "GK-ADMIN-ENTERED-ACCESS-KEY"
	adminSecretAccessKey   = "admin-entered-secret-access-key-00000"
	adminMaintenanceKeyID  = "GK-ADMIN-ENTERED-MAINTENANCE-KEY"
	adminMaintenanceSecret = "admin-entered-maintenance-secret-0000"
)

// adminTestEnv wires a server with the admin surface available and one target
// already stored, so read and write paths both have something to work on.
type adminTestEnv struct {
	srv     *Server
	store   *targets.MemoryStore
	sealer  targets.CredSealer
	configs spacecfg.Store
	now     time.Time
	// checks records every connection check the handler asked for, so a test
	// can assert which credentials went out.
	checks   []objstore.S3Config
	checkOut objstore.CheckOutcome
}

func newAdminTestEnv(t *testing.T, opts ...Option) *adminTestEnv {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()

	twKey, err := keys.GenerateSRWKey()
	if err != nil {
		t.Fatalf("GenerateSRWKey: %v", err)
	}
	sealer, err := targets.NewCredSealer(twKey)
	if err != nil {
		t.Fatalf("NewCredSealer: %v", err)
	}

	env := &adminTestEnv{
		store:    targets.NewMemoryStore(),
		sealer:   sealer,
		configs:  spacecfg.NewMemoryStore(),
		now:      now,
		checkOut: objstore.CheckOK,
	}

	base := []Option{
		WithTokenValidator(fakeValidator{tokens: map[string]string{
			adminToken: "admin-sub",
			userToken:  "user-sub",
		}}),
		WithAdminResolver(AdminResolverFunc(func(_ context.Context, id Identity) (bool, error) {
			return id.Subject == "admin-sub", nil
		})),
		WithTargetStore(env.store),
		WithAuthorizer(env.store),
		WithCredSealer(sealer),
		WithSpaceConfigStore(env.configs),
		WithClock(func() time.Time { return now }),
		WithTargetChecker(objstore.CheckerFunc(
			func(_ context.Context, cfg objstore.S3Config, _ string) objstore.CheckOutcome {
				env.checks = append(env.checks, cfg)
				return env.checkOut
			})),
	}
	env.srv = NewServer(append(base, opts...)...)
	return env
}

// seedTarget stores a target with real sealed credentials.
func (e *adminTestEnv) seedTarget(t *testing.T, id string) targets.Target {
	t.Helper()
	blob, version, err := e.sealer.Seal(targets.CredentialSet{
		Backup: targets.PlainCreds{
			AccessKeyID:     adminAccessKeyID,
			SecretAccessKey: adminSecretAccessKey,
		},
		Maintenance: targets.PlainCreds{
			AccessKeyID:     adminMaintenanceKeyID,
			SecretAccessKey: adminMaintenanceSecret,
		},
	})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	stored, err := e.store.CreateTarget(context.Background(), targets.Target{
		ID: id, Name: "Buddy S3",
		Endpoint: "garage.internal:3900", Bucket: "household-backups", Prefix: "vault/",
		UsePathStyle: true, DisableTLS: true,
		WrappedCreds: blob, Version: version, MaintenanceConfigured: true,
		CreatedAt: e.now, UpdatedAt: e.now,
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	return stored
}

func (e *adminTestEnv) as(token, method, path string, body []byte) *httptest.ResponseRecorder {
	return doJSON(e.srv, method, path, token, body)
}

// --- the admin route table --------------------------------------------------

// adminRoute is one entry of the admin route table.
type adminRoute struct {
	name   string
	method string
	path   string
	body   []byte
}

// adminRouteTable is the admin surface, expressed as a test fixture. It plays
// the part roleTable() plays for the space-scoped routes: a route added without
// a row here is the mistake these tests exist to catch, because every one of
// them is reachable only by an admin and none of them may return a credential.
func adminRouteTable(targetID string) []adminRoute {
	create := []byte(`{"name":"New","endpoint":"garage.internal:3900","bucket":"b",` +
		`"credentials":{"access_key_id":"` + adminAccessKeyID +
		`","secret_access_key":"` + adminSecretAccessKey + `"}}`)

	return []adminRoute{
		{"list targets", http.MethodGet, "/api/v1/admin/targets", nil},
		{"create target", http.MethodPost, "/api/v1/admin/targets", create},
		{"check target", http.MethodPost, "/api/v1/admin/targets/check", create},
		{"get target", http.MethodGet, "/api/v1/admin/targets/" + targetID, nil},
		{"update target", http.MethodPut, "/api/v1/admin/targets/" + targetID,
			[]byte(`{"name":"Renamed","endpoint":"garage.internal:3900","bucket":"b"}`)},
		{"list grants", http.MethodGet, "/api/v1/admin/targets/" + targetID + "/grants", nil},
		{"replace grants", http.MethodPut, "/api/v1/admin/targets/" + targetID + "/grants",
			[]byte(`{"grants":[{"scope":"all_users"}]}`)},
		// Delete is last: the rows above need the target to still exist.
		{"delete target", http.MethodDelete, "/api/v1/admin/targets/" + targetID, nil},
	}
}

// TestAdminRouteTable_EveryRouteIsAdminOnly walks the whole surface for each
// class of caller. The gate is middleware, so this is not testing one handler
// eight times: it is testing that all eight are actually behind it.
func TestAdminRouteTable_EveryRouteIsAdminOnly(t *testing.T) {
	for _, r := range adminRouteTable("t1") {
		t.Run(r.name, func(t *testing.T) {
			env := newAdminTestEnv(t)
			env.seedTarget(t, "t1")

			if rec := env.as("", r.method, r.path, r.body); rec.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated = %d, want 401: %s", rec.Code, rec.Body.String())
			}
			if rec := env.as(userToken, r.method, r.path, r.body); rec.Code != http.StatusForbidden {
				t.Fatalf("non-admin = %d, want 403: %s", rec.Code, rec.Body.String())
			}

			rec := env.as(adminToken, r.method, r.path, r.body)
			if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized ||
				rec.Code == http.StatusNotFound {
				t.Fatalf("admin = %d, want the route to work: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// An admin path nobody implemented is still behind the gate, and still a 404
// rather than a hole.
func TestAdminUnknownPathIsGatedAndNotFound(t *testing.T) {
	env := newAdminTestEnv(t)
	if rec := env.as(userToken, http.MethodGet, "/api/v1/admin/secrets", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin on an unknown admin path = %d, want 403", rec.Code)
	}
	if rec := env.as(adminToken, http.MethodGet, "/api/v1/admin/secrets", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("admin on an unknown admin path = %d, want 404", rec.Code)
	}
}

// --- target CRUD ------------------------------------------------------------

type adminTargetBody struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Endpoint              string `json:"endpoint"`
	Region                string `json:"region"`
	Bucket                string `json:"bucket"`
	Prefix                string `json:"prefix"`
	UsePathStyle          bool   `json:"use_path_style"`
	DisableTLS            bool   `json:"disable_tls"`
	MaintenanceConfigured bool   `json:"maintenance_configured"`
}

func TestAdminCreateTargetSealsCredentialsAndReturnsNone(t *testing.T) {
	env := newAdminTestEnv(t)

	rec := env.as(adminToken, http.MethodPost, "/api/v1/admin/targets", []byte(`{
		"name":"Buddy","endpoint":"garage.internal:3900","region":"garage","bucket":"backups",
		"prefix":"vault/","use_path_style":true,"disable_tls":true,
		"credentials":{"access_key_id":"`+adminAccessKeyID+`","secret_access_key":"`+adminSecretAccessKey+`"},
		"maintenance_credentials":{"access_key_id":"`+adminMaintenanceKeyID+`","secret_access_key":"`+adminMaintenanceSecret+`"}
	}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}

	var got adminTargetBody
	decodeBody(t, rec.Body, &got)
	if got.ID == "" {
		t.Fatal("create returned no id")
	}
	if got.Name != "Buddy" || got.Bucket != "backups" || got.Prefix != "vault/" ||
		!got.UsePathStyle || !got.DisableTLS || got.Region != "garage" {
		t.Fatalf("create echoed the wrong configuration: %+v", got)
	}
	if !got.MaintenanceConfigured {
		t.Fatal("a target created with two key pairs must report as separated")
	}

	// The credentials reached the store sealed, and the admin can open nothing.
	stored, err := env.store.GetTarget(context.Background(), got.ID)
	if err != nil {
		t.Fatalf("GetTarget: %v", err)
	}
	for _, secret := range []string{adminAccessKeyID, adminSecretAccessKey,
		adminMaintenanceKeyID, adminMaintenanceSecret} {
		if strings.Contains(string(stored.WrappedCreds), secret) {
			t.Fatalf("a credential was stored in the clear: %q", secret)
		}
	}
	opened, err := env.sealer.Open(stored.WrappedCreds)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened.For(targets.RoleBackup).AccessKeyID != adminAccessKeyID ||
		opened.For(targets.RoleMaintenance).AccessKeyID != adminMaintenanceKeyID {
		t.Fatalf("the sealed blob holds the wrong credentials: %+v", opened)
	}
	if stored.CreatedAt != env.now || stored.UpdatedAt != env.now {
		t.Fatalf("timestamps = %v / %v", stored.CreatedAt, stored.UpdatedAt)
	}
}

// Two creates must not collide, and an admin must not be able to choose an id
// that overwrites an existing target.
func TestAdminCreateMintsAFreshIDEachTime(t *testing.T) {
	env := newAdminTestEnv(t)
	body := []byte(`{"name":"Buddy","endpoint":"e","bucket":"b","id":"chosen",
		"credentials":{"access_key_id":"k","secret_access_key":"s"}}`)

	seen := map[string]bool{}
	for range 3 {
		rec := env.as(adminToken, http.MethodPost, "/api/v1/admin/targets", body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
		}
		var got adminTargetBody
		decodeBody(t, rec.Body, &got)
		if got.ID == "chosen" {
			t.Fatal("the client chose the target id")
		}
		if seen[got.ID] {
			t.Fatalf("id %q was minted twice", got.ID)
		}
		seen[got.ID] = true
	}

	all, err := env.store.ListTargets(context.Background())
	if err != nil || len(all) != 3 {
		t.Fatalf("stored targets = %d (%v), want 3", len(all), err)
	}
}

// The property the admin UI depends on: editing a target's configuration
// without re-entering its secret must not disturb the stored credentials.
func TestAdminUpdateWithoutCredentialsPreservesThem(t *testing.T) {
	env := newAdminTestEnv(t)
	seeded := env.seedTarget(t, "t1")

	rec := env.as(adminToken, http.MethodPut, "/api/v1/admin/targets/t1", []byte(`{
		"name":"Renamed","endpoint":"new.internal:3900","bucket":"other-bucket"
	}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}

	stored, err := env.store.GetTarget(context.Background(), "t1")
	if err != nil {
		t.Fatalf("GetTarget: %v", err)
	}
	if stored.Name != "Renamed" || stored.Bucket != "other-bucket" {
		t.Fatalf("the edit was not applied: %+v", stored)
	}
	if string(stored.WrappedCreds) != string(seeded.WrappedCreds) {
		t.Fatal("a metadata edit rewrote the sealed credentials")
	}
	if !stored.MaintenanceConfigured {
		t.Fatal("a metadata edit forgot that the target is credential-separated")
	}
	if stored.CreatedAt != seeded.CreatedAt {
		t.Fatalf("created_at changed on update: %v", stored.CreatedAt)
	}
}

// The other half of the same rule, and the one a UI must warn about: the sealed
// set is indivisible, so submitting the backup pair alone replaces the whole
// set and drops the maintenance pair. Merging would need a decrypt the admin
// path does not have (decisions.md #14).
func TestAdminUpdateWithCredentialsReplacesTheWholeSet(t *testing.T) {
	env := newAdminTestEnv(t)
	env.seedTarget(t, "t1")

	rec := env.as(adminToken, http.MethodPut, "/api/v1/admin/targets/t1", []byte(`{
		"name":"Buddy","endpoint":"garage.internal:3900","bucket":"household-backups",
		"credentials":{"access_key_id":"GK-ROTATED","secret_access_key":"rotated-secret-00000000000000000000"}
	}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}

	stored, err := env.store.GetTarget(context.Background(), "t1")
	if err != nil {
		t.Fatalf("GetTarget: %v", err)
	}
	if stored.MaintenanceConfigured {
		t.Fatal("the target still claims a maintenance credential it no longer has")
	}
	opened, err := env.sealer.Open(stored.WrappedCreds)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened.For(targets.RoleBackup).AccessKeyID != "GK-ROTATED" {
		t.Fatalf("the backup credential was not replaced: %+v", opened.For(targets.RoleBackup))
	}
	if opened.Separated() {
		t.Fatal("the maintenance credential survived a whole-set replacement")
	}
}

func TestAdminTargetValidation(t *testing.T) {
	creds := `"credentials":{"access_key_id":"k","secret_access_key":"s"}`

	cases := []struct {
		name string
		body string
		// wants is a fragment the error message must contain, so the admin is
		// told which field they got wrong.
		wants string
	}{
		{"no name", `{"endpoint":"e","bucket":"b",` + creds + `}`, "name"},
		{"no endpoint", `{"name":"n","bucket":"b",` + creds + `}`, "endpoint"},
		{"no bucket", `{"name":"n","endpoint":"e",` + creds + `}`, "bucket"},
		{"blank name", `{"name":"   ","endpoint":"e","bucket":"b",` + creds + `}`, "name"},
		{"no credentials on create", `{"name":"n","endpoint":"e","bucket":"b"}`, "credentials"},
		{"half a credential", `{"name":"n","endpoint":"e","bucket":"b",` +
			`"credentials":{"access_key_id":"k"}}`, "secret access key"},
		{"half a maintenance credential", `{"name":"n","endpoint":"e","bucket":"b",` + creds +
			`,"maintenance_credentials":{"access_key_id":"k"}}`, "maintenance"},
		{"maintenance credentials alone", `{"name":"n","endpoint":"e","bucket":"b",` +
			`"maintenance_credentials":{"access_key_id":"k","secret_access_key":"s"}}`, "maintenance"},
		{"not json", `{`, "malformed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newAdminTestEnv(t)
			rec := env.as(adminToken, http.MethodPost, "/api/v1/admin/targets", []byte(tc.body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wants) {
				t.Fatalf("error does not mention %q: %s", tc.wants, rec.Body.String())
			}
			if all, err := env.store.ListTargets(context.Background()); err != nil || len(all) != 0 {
				t.Fatalf("a refused create stored something: %+v (%v)", all, err)
			}
		})
	}
}

// A half-configured maintenance pair is refused on update too: silently falling
// back produces a deployment that looks separated and is not.
func TestAdminUpdateRefusesHalfAMaintenanceCredential(t *testing.T) {
	env := newAdminTestEnv(t)
	env.seedTarget(t, "t1")
	rec := env.as(adminToken, http.MethodPut, "/api/v1/admin/targets/t1", []byte(`{
		"name":"n","endpoint":"e","bucket":"b",
		"credentials":{"access_key_id":"k","secret_access_key":"s"},
		"maintenance_credentials":{"secret_access_key":"s"}
	}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminTargetNotFoundIsUniform(t *testing.T) {
	env := newAdminTestEnv(t)
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/targets/absent"},
		{http.MethodPut, "/api/v1/admin/targets/absent"},
		{http.MethodDelete, "/api/v1/admin/targets/absent"},
		{http.MethodGet, "/api/v1/admin/targets/absent/grants"},
		{http.MethodPut, "/api/v1/admin/targets/absent/grants"},
	} {
		body := []byte(`{"name":"n","endpoint":"e","bucket":"b"}`)
		rec := env.as(adminToken, r.method, r.path, body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404: %s", r.method, r.path, rec.Code, rec.Body.String())
		}
	}
}

// --- delete -----------------------------------------------------------------

// Deleting a target Spaces still back up to would stop those backups silently.
// The refusal carries a count and never a Space id: an admin may know how much
// work this would cause and may not learn whose (decisions.md #15).
func TestAdminDeleteRefusesATargetStillInUse(t *testing.T) {
	env := newAdminTestEnv(t)
	env.seedTarget(t, "t1")
	ctx := context.Background()
	for _, spaceID := range []string{"space-alice", "space-family"} {
		if _, err := env.configs.Put(ctx, spacecfg.Config{
			SpaceID: spaceID, TargetID: "t1",
			RetentionWindow: 30 * 24 * time.Hour, Enabled: true,
		}); err != nil {
			t.Fatalf("Put config: %v", err)
		}
	}

	rec := env.as(adminToken, http.MethodDelete, "/api/v1/admin/targets/t1", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "2") {
		t.Fatalf("the refusal does not say how many spaces: %s", body)
	}
	for _, spaceID := range []string{"space-alice", "space-family"} {
		if strings.Contains(body, spaceID) {
			t.Fatalf("the refusal named a space: %s", body)
		}
	}
	if _, err := env.store.GetTarget(ctx, "t1"); err != nil {
		t.Fatalf("the target was deleted anyway: %v", err)
	}
}

func TestAdminDeleteRemovesTargetAndGrants(t *testing.T) {
	env := newAdminTestEnv(t)
	env.seedTarget(t, "t1")
	ctx := context.Background()
	if err := env.store.PutGrant(ctx, targets.Grant{
		TargetID: "t1", Scope: targets.ScopeAllUsers,
	}); err != nil {
		t.Fatalf("PutGrant: %v", err)
	}
	// A Space bound to a *different* target must not block this delete.
	if _, err := env.configs.Put(ctx, spacecfg.Config{
		SpaceID: "space-alice", TargetID: "t-other", Enabled: true,
	}); err != nil {
		t.Fatalf("Put config: %v", err)
	}

	rec := env.as(adminToken, http.MethodDelete, "/api/v1/admin/targets/t1", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if _, err := env.store.GetTarget(ctx, "t1"); err == nil {
		t.Fatal("the target survived its delete")
	}
	grants, err := env.store.ListGrants(ctx, "t1")
	if err != nil || len(grants) != 0 {
		t.Fatalf("grants outlived their target: %+v (%v)", grants, err)
	}
}

// A deletion whose consequences cannot be established is refused rather than
// guessed, the same way an unresolvable grant is (decisions.md #20).
func TestAdminDeleteRefusesWhenUsageCannotBeEstablished(t *testing.T) {
	env := newAdminTestEnv(t, WithSpaceConfigStore(nil))
	env.seedTarget(t, "t1")

	rec := env.as(adminToken, http.MethodDelete, "/api/v1/admin/targets/t1", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("delete = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if _, err := env.store.GetTarget(context.Background(), "t1"); err != nil {
		t.Fatalf("the target was deleted anyway: %v", err)
	}
}

// --- grants -----------------------------------------------------------------

type grantsBody struct {
	Grants []grantDTO `json:"grants"`
}

// The point of the grant API: what an admin writes here is what a user sees in
// GET /api/v1/targets, which is the enforcement boundary of decisions.md #12.
func TestAdminGrantsDriveWhatAUserSees(t *testing.T) {
	env := newAdminTestEnv(t, WithSpaceReader(fakeSpaceReader{spaces: []cs3.Space{{
		ID: "space-user", Name: "User", Type: "personal", Owner: "user-sub",
	}}}))
	env.seedTarget(t, "t1")

	// No grants: the user sees nothing.
	if got := visibleTargetIDs(t, env.srv, userToken); len(got) != 0 {
		t.Fatalf("an ungranted target was visible: %v", got)
	}

	rec := env.as(adminToken, http.MethodPut, "/api/v1/admin/targets/t1/grants",
		[]byte(`{"grants":[{"scope":"user","user_sub":"user-sub"}]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("replace grants = %d: %s", rec.Code, rec.Body.String())
	}
	var got grantsBody
	decodeBody(t, rec.Body, &got)
	if len(got.Grants) != 1 || got.Grants[0].Scope != "user" || got.Grants[0].UserSub != "user-sub" {
		t.Fatalf("grants = %+v", got.Grants)
	}
	if ids := visibleTargetIDs(t, env.srv, userToken); len(ids) != 1 || ids[0] != "t1" {
		t.Fatalf("the granted target is not visible to the user: %v", ids)
	}

	// Revoking everything hides it again without deleting the target.
	if rec := env.as(adminToken, http.MethodPut, "/api/v1/admin/targets/t1/grants",
		[]byte(`{"grants":[]}`)); rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d: %s", rec.Code, rec.Body.String())
	}
	if got := visibleTargetIDs(t, env.srv, userToken); len(got) != 0 {
		t.Fatalf("a revoked target is still visible: %v", got)
	}
	if _, err := env.store.GetTarget(context.Background(), "t1"); err != nil {
		t.Fatalf("revoking every grant deleted the target: %v", err)
	}
}

func TestAdminGrantsRoundTripEveryScope(t *testing.T) {
	env := newAdminTestEnv(t)
	env.seedTarget(t, "t1")

	rec := env.as(adminToken, http.MethodPut, "/api/v1/admin/targets/t1/grants", []byte(`{"grants":[
		{"scope":"all_users"},
		{"scope":"user","user_sub":"alice"},
		{"scope":"space","space_id":"space-family"}
	]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("replace = %d: %s", rec.Code, rec.Body.String())
	}

	read := env.as(adminToken, http.MethodGet, "/api/v1/admin/targets/t1/grants", nil)
	var got grantsBody
	decodeBody(t, read.Body, &got)
	if len(got.Grants) != 3 {
		t.Fatalf("grants = %+v, want 3", got.Grants)
	}
	want := map[string]grantDTO{
		"all_users": {Scope: "all_users"},
		"user":      {Scope: "user", UserSub: "alice"},
		"space":     {Scope: "space", SpaceID: "space-family"},
	}
	for _, g := range got.Grants {
		if want[g.Scope] != g {
			t.Fatalf("grant %+v does not round-trip", g)
		}
	}
}

func TestAdminGrantValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		// The empty scope is the interesting one: a client that forgets the
		// field must not be read as "everyone".
		{"missing scope", `{"grants":[{"user_sub":"alice"}]}`},
		{"unknown scope", `{"grants":[{"scope":"everyone"}]}`},
		{"the stored integer is not the wire form", `{"grants":[{"scope":"1"}]}`},
		{"user grant without a user", `{"grants":[{"scope":"user"}]}`},
		{"space grant without a space", `{"grants":[{"scope":"space"}]}`},
		{"all-users grant naming a user", `{"grants":[{"scope":"all_users","user_sub":"alice"}]}`},
		{"malformed body", `{`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newAdminTestEnv(t)
			env.seedTarget(t, "t1")
			if err := env.store.PutGrant(context.Background(), targets.Grant{
				TargetID: "t1", Scope: targets.ScopeUser, UserSub: "alice",
			}); err != nil {
				t.Fatalf("PutGrant: %v", err)
			}

			rec := env.as(adminToken, http.MethodPut, "/api/v1/admin/targets/t1/grants", []byte(tc.body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			// A refused write leaves the audience exactly as it was.
			grants, err := env.store.ListGrants(context.Background(), "t1")
			if err != nil || len(grants) != 1 || grants[0].UserSub != "alice" {
				t.Fatalf("a refused grant write changed the audience: %+v (%v)", grants, err)
			}
		})
	}
}

// A grant body cannot retarget: the target comes from the path.
func TestAdminGrantsCannotNameAnotherTarget(t *testing.T) {
	env := newAdminTestEnv(t)
	env.seedTarget(t, "t1")
	env.seedTarget(t, "t2")

	rec := env.as(adminToken, http.MethodPut, "/api/v1/admin/targets/t1/grants",
		[]byte(`{"grants":[{"target_id":"t2","scope":"all_users"}]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("replace = %d: %s", rec.Code, rec.Body.String())
	}
	other, err := env.store.ListGrants(context.Background(), "t2")
	if err != nil || len(other) != 0 {
		t.Fatalf("a grant landed on another target: %+v (%v)", other, err)
	}
}

func visibleTargetIDs(t *testing.T, srv *Server, token string) []string {
	t.Helper()
	rec := authGet(srv, "/api/v1/targets", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /targets = %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Targets []targetDTO `json:"targets"`
	}
	decodeBody(t, rec.Body, &got)
	ids := make([]string, 0, len(got.Targets))
	for _, tt := range got.Targets {
		ids = append(ids, tt.ID)
	}
	return ids
}

// --- connection check -------------------------------------------------------

type checkBody struct {
	Results []checkResultDTO `json:"results"`
}

func TestAdminCheckUsesTheSubmittedCredentialsPerRole(t *testing.T) {
	env := newAdminTestEnv(t)
	env.seedTarget(t, "t1")

	rec := env.as(adminToken, http.MethodPost, "/api/v1/admin/targets/check", []byte(`{
		"name":"Buddy","endpoint":"garage.internal:3900","bucket":"backups","prefix":"vault/",
		"use_path_style":true,"disable_tls":true,
		"credentials":{"access_key_id":"`+adminAccessKeyID+`","secret_access_key":"`+adminSecretAccessKey+`"},
		"maintenance_credentials":{"access_key_id":"`+adminMaintenanceKeyID+`","secret_access_key":"`+adminMaintenanceSecret+`"}
	}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("check = %d: %s", rec.Code, rec.Body.String())
	}

	var got checkBody
	decodeBody(t, rec.Body, &got)
	if len(got.Results) != 2 {
		t.Fatalf("results = %+v, want one per role", got.Results)
	}
	if got.Results[0].Role != "backup" || got.Results[1].Role != "maintenance" {
		t.Fatalf("roles = %+v", got.Results)
	}
	for _, r := range got.Results {
		if r.Outcome != string(objstore.CheckOK) {
			t.Fatalf("outcome = %q, want %q", r.Outcome, objstore.CheckOK)
		}
	}

	// Each role was checked with its own key, and with the submitted
	// configuration rather than anything stored.
	if len(env.checks) != 2 {
		t.Fatalf("checks = %d, want 2", len(env.checks))
	}
	if env.checks[0].AccessKeyID != adminAccessKeyID ||
		env.checks[1].AccessKeyID != adminMaintenanceKeyID {
		t.Fatalf("the roles were checked with the wrong keys: %+v", env.checks)
	}
	if env.checks[0].Bucket != "backups" || !env.checks[0].UsePathStyle || !env.checks[0].DisableTLS {
		t.Fatalf("the submitted configuration was not used: %+v", env.checks[0])
	}
}

// One credential pair means one check: a single-key target is not
// misconfigured, and reporting a maintenance role it does not have would invite
// an admin to fix something that is not broken.
func TestAdminCheckWithOneCredentialChecksOneRole(t *testing.T) {
	env := newAdminTestEnv(t)
	rec := env.as(adminToken, http.MethodPost, "/api/v1/admin/targets/check", []byte(`{
		"name":"Buddy","endpoint":"e","bucket":"b",
		"credentials":{"access_key_id":"k","secret_access_key":"s"}
	}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("check = %d: %s", rec.Code, rec.Body.String())
	}
	var got checkBody
	decodeBody(t, rec.Body, &got)
	if len(got.Results) != 1 || got.Results[0].Role != "backup" {
		t.Fatalf("results = %+v", got.Results)
	}
}

// The check is a diagnostic, not a write: a failing target is reported, not
// turned into an HTTP error, and the verdict stays coarse.
func TestAdminCheckReportsFailureWithoutDetail(t *testing.T) {
	env := newAdminTestEnv(t)
	env.checkOut = objstore.CheckAuthFailed

	rec := env.as(adminToken, http.MethodPost, "/api/v1/admin/targets/check", []byte(`{
		"name":"Buddy","endpoint":"e","bucket":"b",
		"credentials":{"access_key_id":"k","secret_access_key":"s"}
	}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("check = %d, want 200 with a verdict: %s", rec.Code, rec.Body.String())
	}
	var got checkBody
	decodeBody(t, rec.Body, &got)
	if len(got.Results) != 1 || got.Results[0].Outcome != string(objstore.CheckAuthFailed) {
		t.Fatalf("results = %+v", got.Results)
	}
}

// The check stores nothing. It is a question about credentials the caller
// typed, not a way of creating or editing a target.
func TestAdminCheckIsStateless(t *testing.T) {
	env := newAdminTestEnv(t)
	rec := env.as(adminToken, http.MethodPost, "/api/v1/admin/targets/check", []byte(`{
		"name":"Buddy","endpoint":"e","bucket":"b",
		"credentials":{"access_key_id":"k","secret_access_key":"s"}
	}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("check = %d: %s", rec.Code, rec.Body.String())
	}
	all, err := env.store.ListTargets(context.Background())
	if err != nil || len(all) != 0 {
		t.Fatalf("the check stored a target: %+v (%v)", all, err)
	}
}

// --- unwired dependencies ---------------------------------------------------

// Without a target store the admin surface is unavailable, not broken: 503
// says "this deployment has not wired it", which is what an operator needs.
func TestAdminSurfaceIsUnavailableWithoutAStore(t *testing.T) {
	env := newAdminTestEnv(t, WithTargetStore(nil))
	for _, r := range adminRouteTable("t1") {
		if r.name == "check target" {
			continue // the check needs no store; see the test below
		}
		rec := env.as(adminToken, r.method, r.path, r.body)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s = %d, want 503: %s", r.name, rec.Code, rec.Body.String())
		}
	}
}

// Without the TW key credentials cannot be sealed — but the configuration and
// the audience are still readable and editable. An operator whose TW_KEY is
// missing needs the admin UI to work in order to find that out.
func TestAdminWithoutASealerRefusesCredentialsOnly(t *testing.T) {
	env := newAdminTestEnv(t, WithCredSealer(nil))
	env.seedTarget(t, "t1")

	withCreds := []byte(`{"name":"n","endpoint":"e","bucket":"b",
		"credentials":{"access_key_id":"k","secret_access_key":"s"}}`)
	if rec := env.as(adminToken, http.MethodPost, "/api/v1/admin/targets",
		withCreds); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("create = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if rec := env.as(adminToken, http.MethodPut, "/api/v1/admin/targets/t1",
		withCreds); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("update with credentials = %d, want 503: %s", rec.Code, rec.Body.String())
	}

	if rec := env.as(adminToken, http.MethodGet, "/api/v1/admin/targets", nil); rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if rec := env.as(adminToken, http.MethodPut, "/api/v1/admin/targets/t1",
		[]byte(`{"name":"Renamed","endpoint":"e","bucket":"b"}`)); rec.Code != http.StatusOK {
		t.Fatalf("metadata update = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if rec := env.as(adminToken, http.MethodPut, "/api/v1/admin/targets/t1/grants",
		[]byte(`{"grants":[{"scope":"all_users"}]}`)); rec.Code != http.StatusOK {
		t.Fatalf("grants = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminCheckIsUnavailableWithoutAChecker(t *testing.T) {
	env := newAdminTestEnv(t, WithTargetChecker(nil))
	rec := env.as(adminToken, http.MethodPost, "/api/v1/admin/targets/check", []byte(`{
		"name":"n","endpoint":"e","bucket":"b",
		"credentials":{"access_key_id":"k","secret_access_key":"s"}
	}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("check = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}
