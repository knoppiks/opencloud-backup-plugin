//go:build integration

package cs3_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"opencloud-backup-plugin/pkg/cs3"
)

// fixtureClient dials the live OpenCloud fixture with the seeded service
// account, skipping the test when the fixture env is not sourced.
func fixtureClient(t *testing.T) (*cs3.Client, context.Context) {
	t.Helper()
	addr := os.Getenv("CS3_GATEWAY_ADDR")
	saID := os.Getenv("CS3_SERVICE_ACCOUNT_ID")
	saSecret := os.Getenv("CS3_SERVICE_ACCOUNT_SECRET")
	if addr == "" || saID == "" || saSecret == "" {
		t.Skip("CS3 fixture env not set; source test/fixtures/opencloud/fixture.env")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial gateway %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	gw := gateway.NewGatewayAPIClient(conn)
	return cs3.NewClient(gw, cs3.ServiceAccountAuth{
		Gateway:  gw,
		ClientID: saID,
		Secret:   saSecret,
	}), ctx
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
	sharedID := os.Getenv("OC_SHARED_SPACE_ID")
	if sharedID == "" {
		t.Skip("OC_SHARED_SPACE_ID not set; re-run test/fixtures/opencloud/seed.sh")
	}
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
