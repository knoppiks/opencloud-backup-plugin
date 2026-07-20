//go:build integration

package cs3_test

import (
	"context"
	"os"
	"testing"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"opencloud-backup-plugin/pkg/cs3"
)

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
	addr := os.Getenv("CS3_GATEWAY_ADDR")
	saID := os.Getenv("CS3_SERVICE_ACCOUNT_ID")
	saSecret := os.Getenv("CS3_SERVICE_ACCOUNT_SECRET")
	adminUID := os.Getenv("OC_ADMIN_USER_ID")
	if addr == "" || saID == "" || saSecret == "" {
		t.Skip("CS3 fixture env not set; source test/fixtures/opencloud/fixture.env")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial gateway %s: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()

	gw := gateway.NewGatewayAPIClient(conn)
	client := cs3.NewClient(gw, cs3.ServiceAccountAuth{
		Gateway:  gw,
		ClientID: saID,
		Secret:   saSecret,
	})

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
