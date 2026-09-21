//go:build integration

package cs3_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/cs3"
)

// fixtureClient dials the live OpenCloud fixture with the seeded service
// account, skipping the test when the fixture env is not sourced.
func fixtureClient(t *testing.T) (*cs3.Client, context.Context) {
	t.Helper()
	env := testutil.OpenCloudEnv(t,
		"CS3_GATEWAY_ADDR", "CS3_SERVICE_ACCOUNT_ID", "CS3_SERVICE_ACCOUNT_SECRET")
	addr, saID, saSecret := env[0], env[1], env[2]

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial gateway %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	gw := gateway.NewGatewayAPIClient(conn)
	return cs3.NewClient(gw,
		cs3.ServiceAccountAuth{Gateway: gw, ClientID: saID, Secret: saSecret},
		// The fixture's data gateway serves a self-signed certificate.
		cs3.WithHTTPClient(&http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		}),
	), ctx
}

// TestListSpacesIntegration runs the Phase-2 CS3 read slice against the live
// OpenCloud fixture (test/fixtures/opencloud/, brought up via up.sh + seed.sh).
// It exercises the service-account auth + ListStorageSpaces path the API's
// GET /spaces depends on, and asserts the seeded admin's personal space is
// present with the expected owner.
//
// Config comes from fixture.env (source it before running):
//
//	CS3_GATEWAY_ADDR, CS3_SERVICE_ACCOUNT_ID, CS3_SERVICE_ACCOUNT_SECRET,
//	OC_ADMIN_USER_ID (the seeded admin's user id / space owner)
//
//	Run: source test/fixtures/opencloud/fixture.env && \
//	     go test -tags integration ./pkg/cs3/...
func TestListSpacesIntegration(t *testing.T) {
	adminUID := os.Getenv("OC_ADMIN_USER_ID")
	client, ctx := fixtureClient(t)

	spaces, err := client.ListSpaces(ctx)
	if err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	if len(spaces) == 0 {
		t.Fatal("expected at least one space from the worker credential")
	}

	var foundPersonal bool
	for _, s := range spaces {
		t.Logf("space id=%s name=%q type=%s owner=%s members=%d",
			s.ID, s.Name, s.Type, s.Owner, len(s.Members))
		if s.Type == "personal" && (adminUID == "" || s.Owner == adminUID) {
			foundPersonal = true
		}
	}
	if !foundPersonal {
		t.Fatalf("expected the seeded admin's personal space (owner=%s) in the list", adminUID)
	}
}

// TestIntegration_SpaceGrantsShape pins the CS3 grants opaque map against real
// reva output (remediation R3, task 1). The whole authorization model rests on
// this map: a grant carries a *permission set*, not a role name, plus sidecar
// maps saying which principals are groups and when a grant lapses. If a reva
// upgrade changes any of that, this test fails — rather than an authorization
// decision silently becoming wrong.
//
// The Space and its grants come from test/fixtures/opencloud/seed.sh.
func TestIntegration_SpaceGrantsShape(t *testing.T) {
	sharedID := testutil.OpenCloudEnv(t, "OC_SHARED_SPACE_ID")[0]
	var (
		viewer  = os.Getenv("OC_SHARED_VIEWER_ID")
		editor  = os.Getenv("OC_SHARED_EDITOR_ID")
		manager = os.Getenv("OC_SHARED_MANAGER_ID")
		grouped = os.Getenv("OC_SHARED_GROUPED_ID")
		groupID = os.Getenv("OC_SHARED_GROUP_ID")
	)

	client, ctx := fixtureClient(t)
	spaces, err := client.ListSpaces(ctx)
	if err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}

	// The graph drive id is a prefix of the CS3 space id: graph reports
	// "storageid$spaceid", CS3 "storageid$spaceid!opaqueid".
	var shared cs3.Space
	for _, s := range spaces {
		if strings.HasPrefix(s.ID, sharedID) {
			shared = s
			break
		}
	}
	if shared.ID == "" {
		t.Fatalf("seeded shared space %q not visible to the worker credential", sharedID)
	}

	// Roles are derived from permission sets, so this asserts the whole chain:
	// opaque map -> permission set -> role.
	wantRoles := map[string]cs3.Role{
		viewer:  cs3.RoleViewer,
		editor:  cs3.RoleEditor,
		manager: cs3.RoleManager,
		groupID: cs3.RoleViewer,
	}
	for principal, want := range wantRoles {
		got, ok := shared.Members[principal]
		if !ok {
			t.Errorf("principal %s missing from grants: %+v", principal, shared.Members)
			continue
		}
		if got.Role != want {
			t.Errorf("principal %s: got role %v, want %v", principal, got.Role, want)
		}
	}

	// The groups sidecar must distinguish the group grant from the user grants,
	// or a group id would be matched against caller subjects and vice versa.
	if !shared.Members[groupID].Group {
		t.Errorf("group %s not marked as a group grant: %+v", groupID, shared.Members[groupID])
	}
	if shared.Members[viewer].Group {
		t.Errorf("user %s marked as a group grant", viewer)
	}
	if !shared.GroupGrants() {
		t.Error("GroupGrants() = false for a space carrying a group grant")
	}

	// The expiring grant must surface its expiry; without it an expired grant
	// would read as permanent.
	if shared.Members[editor].ExpiresAt.IsZero() {
		t.Errorf("editor grant lost its expiry: %+v", shared.Members[editor])
	}

	now := time.Now()
	if got := shared.RoleFor(editor, nil, now); got != cs3.RoleEditor {
		t.Errorf("RoleFor(editor) = %v, want editor while the grant is live", got)
	}
	if got := shared.RoleFor(editor, nil, shared.Members[editor].ExpiresAt); got != cs3.RoleNone {
		t.Errorf("RoleFor(editor) at expiry = %v, want none", got)
	}
	if got := shared.RoleFor(grouped, nil, now); got != cs3.RoleNone {
		t.Errorf("RoleFor(group member, no groups) = %v, want none", got)
	}
	if got := shared.RoleFor(grouped, []string{groupID}, now); got != cs3.RoleViewer {
		t.Errorf("RoleFor(group member, via group) = %v, want viewer", got)
	}
	if got := shared.RoleFor("no-such-user", nil, now); got != cs3.RoleNone {
		t.Errorf("RoleFor(stranger) = %v, want none", got)
	}
}

// TestIntegration_ZeroByteUploadCompletesAtInitiation pins the one upload
// behaviour that cost a bug: reva finishes a zero-length upload inside
// InitiateFileUpload — the file exists, with the mtime that was asked for,
// before any body is sent — and then answers the PUT that would follow with
// 500 "upload not found".
//
// The client therefore sends no body for an empty file (writer.go). If a future
// OpenCloud starts expecting one, this test says so; without it, the symptom
// would be every restore failing on the first empty file it meets, which is a
// thing real Spaces are full of.
func TestIntegration_ZeroByteUpload(t *testing.T) {
	client, ctx := fixtureClient(t)
	space := writableFixtureSpace(ctx, t, client)

	dir := fmt.Sprintf("zero-byte-%d", time.Now().UnixNano())
	if err := client.MakeDir(ctx, space, dir); err != nil {
		t.Fatalf("MakeDir: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := client.Delete(cleanupCtx, space, dir); err != nil {
			t.Logf("could not remove %s: %v", dir, err)
		}
	})

	mtime := time.Date(2019, 11, 3, 14, 27, 53, 0, time.UTC)
	rel := path.Join(dir, "empty.txt")
	if err := client.Upload(ctx, space, rel, 0, mtime, bytes.NewReader(nil)); err != nil {
		t.Fatalf("Upload(size=0): %v", err)
	}

	entries, err := client.ListDir(ctx, space, dir)
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	var found *cs3.Entry
	for i := range entries {
		if path.Base(entries[i].Path) == "empty.txt" {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatalf("the empty file was not created; entries = %+v", entries)
	}
	if found.Size != 0 || found.IsDir {
		t.Fatalf("empty file = %+v, want a zero-byte file", *found)
	}
	if got := time.Unix(found.MTimeUnix, 0).UTC(); !got.Equal(mtime) {
		t.Errorf("mtime = %s, want %s: the requested mtime survived initiation before, "+
			"so losing it here is a change in reva worth noticing", got, mtime)
	}
}

// writableFixtureSpace picks a Space the service account may write to,
// preferring the seeded project Space.
func writableFixtureSpace(ctx context.Context, t *testing.T, client *cs3.Client) cs3.Space {
	t.Helper()

	spaces, err := client.ListSpaces(ctx)
	if err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	if len(spaces) == 0 {
		testutil.FixtureGap(t, "no space visible to the service account")
	}
	if shared := strings.TrimSpace(os.Getenv("OC_SHARED_SPACE_ID")); shared != "" {
		for _, s := range spaces {
			if strings.HasPrefix(s.ID, shared) {
				return s
			}
		}
	}
	return spaces[0]
}
