//go:build integration

package backup

// Which durable store the Phase-6 exit criteria run against (remediation R9).
//
// The criteria are statements about durability: a run happens with nobody
// logged in, a restart loses neither the history nor the Space, a crashed run
// is recovered, a Space that stops backing up is reported. All four were only
// ever exercised over the in-memory store — which is not what a deployment
// runs, and which cannot exhibit the properties that make the real one hard:
// no transactions, no compare-and-set, a network between every read and write.
//
// So each criterion now runs twice: once over memory, and once over an
// OpenCloud Space when the fixture is there. The second is the one that counts;
// the first is kept because it is fast and because a failure in both says
// something different from a failure in one.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/cs3state"
	"opencloud-backup-plugin/pkg/state"
)

// stateBackend is one durable store to hold a criterion against.
type stateBackend struct {
	name string
	open func(t *testing.T) state.Store
}

// forEachStateBackend runs body once per backend, as a subtest named after it.
func forEachStateBackend(t *testing.T, body func(t *testing.T, backing state.Store)) {
	t.Helper()

	backends := []stateBackend{
		{name: "memory", open: func(*testing.T) state.Store { return state.NewMemoryStore() }},
		{name: "opencloud", open: openCloudStateStore},
	}
	for _, backend := range backends {
		t.Run(backend.name, func(t *testing.T) {
			body(t, backend.open(t))
		})
	}
}

// openCloudStateStore builds the real thing: the service's state in an
// OpenCloud Space, over CS3, under a prefix of this run's own so a long-lived
// fixture does not accumulate rubbish.
func openCloudStateStore(t *testing.T) state.Store {
	t.Helper()

	env := testutil.OpenCloudEnv(t,
		"CS3_GATEWAY_ADDR", "CS3_SERVICE_ACCOUNT_ID", "CS3_SERVICE_ACCOUNT_SECRET")
	addr, saID, saSecret := env[0], env[1], env[2]

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial gateway %s: %v", addr, err)
	}
	// Registered first so it runs last: the connection has to outlive the
	// cleanup that uses it.
	t.Cleanup(func() { _ = conn.Close() })

	gw := gateway.NewGatewayAPIClient(conn)
	client := cs3.NewClient(gw,
		cs3.ServiceAccountAuth{Gateway: gw, ClientID: saID, Secret: saSecret},
		cs3.WithHTTPClient(&http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	spaces, err := client.ListSpaces(ctx)
	if err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	if len(spaces) == 0 {
		testutil.FixtureGap(t, "no space visible to the service account")
	}
	spaceID := spaces[0].ID
	if shared := strings.TrimSpace(os.Getenv("OC_SHARED_SPACE_ID")); shared != "" {
		for _, s := range spaces {
			if strings.HasPrefix(s.ID, shared) {
				spaceID = s.ID
			}
		}
	}

	store, err := cs3state.New(client, cs3state.Options{
		SpaceID: spaceID,
		Prefix:  fmt.Sprintf(".backup-service-state-test-%d", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("cs3state.New: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		keys, err := store.List(cleanupCtx, "")
		if err != nil {
			t.Logf("cleanup listing failed: %v", err)
			return
		}
		for _, key := range keys {
			if err := store.Delete(cleanupCtx, key); err != nil {
				t.Logf("cleanup of %s failed: %v", key, err)
			}
		}
	})

	return store
}
