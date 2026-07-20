//go:build integration

package api_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/api"
)

// TestAdminAppRoleIDPinnedIntegration guards the admin-role finding
// (phase-0-findings.md): the pinned admin appRoleId must still map admin -> true
// and the seeded normal user -> false on the live OpenCloud fixture. It queries
// the graph API directly (basic auth, enabled for the fixture) rather than
// through the bearer-token resolver, so it needs no OIDC browser flow while
// still catching drift in the pinned role id.
//
// Config from test/fixtures/opencloud/fixture.env + up.sh/seed.sh:
//
//	OC_URL, OC_ADMIN_APP_ROLE_ID, OC_NORMAL_USER_ID
//
//	Run: source test/fixtures/opencloud/fixture.env && \
//	     go test -tags integration ./pkg/api/...
func TestAdminAppRoleIDPinnedIntegration(t *testing.T) {
	base := os.Getenv("OC_URL")
	if base == "" {
		t.Skip("OC_URL not set; source test/fixtures/opencloud/fixture.env")
	}
	adminRoleID := os.Getenv("OC_ADMIN_APP_ROLE_ID")
	if adminRoleID == "" {
		adminRoleID = api.DefaultAdminAppRoleID
	}

	// The fixture uses a self-signed cert; allow it for this local test only.
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Admin (basic auth admin:admin) must carry the pinned admin appRoleId.
	adminRoles := fetchAppRoles(t, ctx, client, base, "admin", "admin")
	if !contains(adminRoles, adminRoleID) {
		t.Fatalf("admin does not carry pinned admin appRoleId %s (got %v)", adminRoleID, adminRoles)
	}

	// Normal user must NOT carry the admin role.
	normalRoles := fetchAppRoles(t, ctx, client, base, "testuser", "Test-User-1!")
	if contains(normalRoles, adminRoleID) {
		t.Fatalf("normal user unexpectedly carries admin appRoleId %s", adminRoleID)
	}
	if len(normalRoles) == 0 {
		t.Fatal("normal user should have a (non-admin) app role; got none — is the fixture seeded?")
	}
}

func fetchAppRoles(t *testing.T, ctx context.Context, client *http.Client, base, user, pass string) []string {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/graph/v1.0/me?$expand=appRoleAssignments", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.SetBasicAuth(user, pass)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("graph /me as %s: %v", user, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("graph /me as %s: status %d", user, resp.StatusCode)
	}
	var body struct {
		AppRoleAssignments []struct {
			AppRoleID string `json:"appRoleId"`
		} `json:"appRoleAssignments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /me as %s: %v", user, err)
	}
	ids := make([]string, 0, len(body.AppRoleAssignments))
	for _, a := range body.AppRoleAssignments {
		ids = append(ids, a.AppRoleID)
	}
	return ids
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
