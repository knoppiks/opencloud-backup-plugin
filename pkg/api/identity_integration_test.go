//go:build integration

package api_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/api"
	"opencloud-backup-plugin/pkg/cs3"
)

// TestRealTokenReachesTheCallersOwnSpace is the test every API test before it
// could not be: a token minted by the fixture's IdP for the web client, the
// real validator, the real user resolver and the real CS3 Space list, and the
// question "does a user see their own personal Space as its owner".
//
// It exists because the answer used to be no. The token's `sub` is not the
// OpenCloud user id, membership was checked against `sub`, and every fake
// validator issued a `sub` equal to the id — so every user was refused every
// Space, and every test passed (pkg/api/users.go).
func TestRealTokenReachesTheCallersOwnSpace(t *testing.T) {
	env := testutil.OpenCloudEnv(t,
		"OC_URL", "OIDC_ISSUER", "OIDC_AUDIENCE", "OC_BASE_URL", "OC_NORMAL_USER_ID",
		"CS3_GATEWAY_ADDR", "CS3_SERVICE_ACCOUNT_ID", "CS3_SERVICE_ACCOUNT_SECRET")
	base, issuer, audience, ocBase, normalUserID := env[0], env[1], env[2], env[3], env[4]
	gwAddr, saID, saSecret := env[5], env[6], env[7]

	// The fixture's certificate is self-signed; this is a local test.
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	token := testutil.FixtureAccessToken(t, client, base, "testuser", "Test-User-1!")

	validator, err := api.NewOIDCValidator(api.OIDCConfig{
		Issuer: issuer, Audience: audience,
		KeySet: api.NewLazyKeySet(issuer, client, time.Hour, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := validator.Validate(ctx, token)
	if err != nil {
		t.Fatalf("the fixture's own token does not validate: %v", err)
	}
	resolver := api.NewGraphUserResolver(ocBase, client)
	userID, err := resolver.UserID(ctx, id)
	if err != nil {
		t.Fatalf("resolve user id: %v", err)
	}
	if userID != normalUserID {
		t.Fatalf("resolved user id %q, want the seeded user's graph id %q", userID, normalUserID)
	}
	t.Logf("token sub equals the OpenCloud user id: %v", id.Subject == userID)

	conn, err := grpc.NewClient(gwAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	defer func() { _ = conn.Close() }()
	gw := gateway.NewGatewayAPIClient(conn)
	spaces := cs3.NewClient(gw, cs3.ServiceAccountAuth{Gateway: gw, ClientID: saID, Secret: saSecret},
		cs3.WithHTTPClient(client))

	srv := api.NewServer(
		api.WithTokenValidator(validator),
		api.WithUserResolver(resolver),
		api.WithSpaceReader(spaces),
		api.WithGroupResolver(api.NewGraphGroupResolver(ocBase, client)),
	)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/spaces", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /spaces = %d", rec.Code)
	}

	var body struct {
		Spaces []struct {
			ID, Name, Type, Role string
		} `json:"spaces"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, sp := range body.Spaces {
		if sp.Type == "personal" && sp.Role == "owner" {
			return
		}
	}
	t.Fatalf("the caller's own personal Space is not listed as theirs; got %+v", body.Spaces)
}
