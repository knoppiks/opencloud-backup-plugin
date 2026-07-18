//go:build integration

// Package main (spikes/cs3) is throwaway Phase-0 exploration code (Spike 3):
// the CS3 read path against a live OpenCloud instance. It validates the single
// riskiest assumption in the plan — that a server-side, unattended worker can
// list Spaces and stream file bytes out of OpenCloud via the CS3 gateway (gRPC)
// plus the HTTP data provider, using a credential suitable for a headless job.
//
// Findings (auth type, token header/lifetime, download flow, space membership)
// land in .agents/plan/phase-0-findings.md. This code is not promoted.
//
// Requires a running OpenCloud fixture (see test/fixtures/opencloud/). Configure
// via env; sensible defaults match the fixture's `up.sh`:
//
//	CS3_GATEWAY_ADDR         host:port of the CS3 gateway gRPC (default 127.0.0.1:9142)
//	CS3_SERVICE_ACCOUNT_ID   OC_SERVICE_ACCOUNT_ID from the generated config
//	CS3_SERVICE_ACCOUNT_SECRET OC_SERVICE_ACCOUNT_SECRET
//	CS3_EXPECT_FILE          space-relative path of the seeded file (default /spike3.txt)
//	CS3_EXPECT_SHA256        expected sha256 of that file
//	CS3_TARGET_USER_ID       user whose spaces to enumerate (the seeded file's owner)
//
// Run: go test -tags integration ./spikes/cs3/...
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// tokenHeader is reva's access-token gRPC metadata key (pkg/ctx.TokenHeader).
const tokenHeader = "x-access-token"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestCS3ReadPath(t *testing.T) {
	gatewayAddr := env("CS3_GATEWAY_ADDR", "127.0.0.1:9142")
	saID := env("CS3_SERVICE_ACCOUNT_ID", "")
	saSecret := env("CS3_SERVICE_ACCOUNT_SECRET", "")
	expectFile := env("CS3_EXPECT_FILE", "/spike3.txt")
	expectSHA := env("CS3_EXPECT_SHA256", "")
	targetUserID := env("CS3_TARGET_USER_ID", "")

	if saID == "" || saSecret == "" {
		t.Skip("CS3_SERVICE_ACCOUNT_ID / CS3_SERVICE_ACCOUNT_SECRET not set; start the OpenCloud fixture and export them")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(gatewayAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial gateway %s: %v", gatewayAddr, err)
	}
	defer conn.Close()
	gw := gateway.NewGatewayAPIClient(conn)

	// --- Step 1: authenticate as the unattended worker -------------------
	// Auth type "serviceaccounts" maps (in OpenCloud's gateway authregistry) to
	// the auth-service provider. The service account authenticates with an
	// owner scope and USER_TYPE_SERVICE, and (per auth-service docs) may stat
	// all files on all spaces — exactly what a headless backup worker needs.
	authRes, err := gw.Authenticate(ctx, &gateway.AuthenticateRequest{
		Type:         "serviceaccounts",
		ClientId:     saID,
		ClientSecret: saSecret,
	})
	if err != nil {
		t.Fatalf("Authenticate RPC: %v", err)
	}
	mustOK(t, authRes.GetStatus(), "Authenticate")
	token := authRes.GetToken()
	if token == "" {
		t.Fatal("Authenticate returned empty token")
	}
	t.Logf("worker authenticated: userType=%s id=%s tokenLen=%d",
		authRes.GetUser().GetId().GetType(), authRes.GetUser().GetId().GetOpaqueId(), len(token))

	// Subsequent calls carry the token in gRPC metadata.
	authCtx := metadata.AppendToOutgoingContext(ctx, tokenHeader, token)

	// --- Step 2: list storage spaces -------------------------------------
	spaces := listSpaces(t, authCtx, gw, targetUserID)
	if len(spaces) == 0 {
		t.Fatal("no storage spaces returned for worker")
	}
	var personal *provider.StorageSpace
	for _, s := range spaces {
		t.Logf("space: type=%-9s name=%-20q id=%s", s.GetSpaceType(), s.GetName(), s.GetId().GetOpaqueId())
		if s.GetSpaceType() == "personal" && personal == nil {
			personal = s
		}
	}
	if personal == nil {
		personal = spaces[0]
	}

	// --- Step 3: walk the space (ListContainer + Stat) -------------------
	root := &provider.Reference{ResourceId: personal.GetRoot()}
	items := listContainer(t, authCtx, gw, root)
	t.Logf("space %q root has %d entries", personal.GetName(), len(items))

	// Locate the seeded file by name.
	target := strings.TrimPrefix(expectFile, "/")
	var fileRef *provider.Reference
	for _, it := range items {
		if strings.EqualFold(baseName(it.GetPath()), target) {
			fileRef = &provider.Reference{ResourceId: it.GetId()}
			break
		}
	}
	if fileRef == nil {
		// Fall back to a path-based reference relative to the space root.
		fileRef = &provider.Reference{
			ResourceId: personal.GetRoot(),
			Path:       "./" + target,
		}
	}

	statRes, err := gw.Stat(authCtx, &provider.StatRequest{Ref: fileRef})
	if err != nil {
		t.Fatalf("Stat RPC: %v", err)
	}
	mustOK(t, statRes.GetStatus(), "Stat")
	info := statRes.GetInfo()
	t.Logf("stat: path=%s size=%d mtime=%v id=%s",
		info.GetPath(), info.GetSize(), info.GetMtime(), info.GetId().GetOpaqueId())

	// --- Step 4: InitiateFileDownload -> stream bytes -> checksum --------
	// Use a space-relative reference (space root id + "./name"). A bare
	// resource-id reference resolves to path "/" on the data server and fails
	// with "resource_id must be set"; the storage-users datatx resolves the
	// target from the reference path carried in the transfer-token claims.
	dlRes, err := gw.InitiateFileDownload(authCtx, &provider.InitiateFileDownloadRequest{
		Ref: &provider.Reference{
			ResourceId: personal.GetRoot(),
			Path:       "./" + target,
		},
	})
	if err != nil {
		t.Fatalf("InitiateFileDownload RPC: %v", err)
	}
	mustOK(t, dlRes.GetStatus(), "InitiateFileDownload")

	var endpoint, transferToken string
	for _, p := range dlRes.GetProtocols() {
		t.Logf("download protocol=%q endpoint=%s tokenLen=%d",
			p.GetProtocol(), p.GetDownloadEndpoint(), len(p.GetToken()))
		if p.GetProtocol() == "spaces" || endpoint == "" {
			endpoint = p.GetDownloadEndpoint()
			transferToken = p.GetToken()
		}
	}
	if endpoint == "" {
		t.Fatal("no download endpoint returned")
	}

	body := streamDownload(t, endpoint, token, transferToken)
	sum := sha256.Sum256(body)
	gotSHA := hex.EncodeToString(sum[:])
	t.Logf("streamed %d bytes sha256=%s", len(body), gotSHA)

	if expectSHA != "" && gotSHA != expectSHA {
		t.Fatalf("checksum mismatch: got %s want %s", gotSHA, expectSHA)
	}

	// --- Step 5: space membership (decisions.md #7) ----------------------
	// How does CS3 expose who may retrieve a shared space's RK? Members/roles
	// live on the space grants. ListStorageSpaces with the right field mask, or
	// Stat on the space root, exposes the permission set / grants.
	logSpaceMembership(t, authCtx, gw, personal)
}

func listSpaces(t *testing.T, ctx context.Context, gw gateway.GatewayAPIClient, targetUserID string) []*provider.StorageSpace {
	t.Helper()
	req := &provider.ListStorageSpacesRequest{}
	if targetUserID != "" {
		// Filter to the target user's spaces via an owner filter.
		req.Filters = []*provider.ListStorageSpacesRequest_Filter{
			{
				Type: provider.ListStorageSpacesRequest_Filter_TYPE_OWNER,
				Term: &provider.ListStorageSpacesRequest_Filter_Owner{
					Owner: &userpb.UserId{OpaqueId: targetUserID},
				},
			},
		}
	}
	res, err := gw.ListStorageSpaces(ctx, req)
	if err != nil {
		t.Fatalf("ListStorageSpaces RPC: %v", err)
	}
	mustOK(t, res.GetStatus(), "ListStorageSpaces")
	return res.GetStorageSpaces()
}

func listContainer(t *testing.T, ctx context.Context, gw gateway.GatewayAPIClient, ref *provider.Reference) []*provider.ResourceInfo {
	t.Helper()
	res, err := gw.ListContainer(ctx, &provider.ListContainerRequest{Ref: ref})
	if err != nil {
		t.Fatalf("ListContainer RPC: %v", err)
	}
	mustOK(t, res.GetStatus(), "ListContainer")
	return res.GetInfos()
}

// streamDownload follows the data-provider URL. Reva's data gateway expects the
// worker access token (x-access-token) and the transfer token
// (x-reva-transfer) as headers.
func streamDownload(t *testing.T, endpoint, accessToken, transferToken string) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatalf("new download request: %v", err)
	}
	req.Header.Set(tokenHeader, accessToken)
	if transferToken != "" {
		req.Header.Set("x-reva-transfer", transferToken)
	}
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   30 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("download GET %s: %v", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		t.Fatalf("download HTTP %d: %s", resp.StatusCode, snippet)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read download body: %v", err)
	}
	return body
}

func logSpaceMembership(t *testing.T, ctx context.Context, gw gateway.GatewayAPIClient, space *provider.StorageSpace) {
	t.Helper()
	// Re-list with grants requested via opaque field so we can observe members.
	res, err := gw.ListStorageSpaces(ctx, &provider.ListStorageSpacesRequest{
		Filters: []*provider.ListStorageSpacesRequest_Filter{
			{
				Type: provider.ListStorageSpacesRequest_Filter_TYPE_ID,
				Term: &provider.ListStorageSpacesRequest_Filter_Id{
					Id: &provider.StorageSpaceId{OpaqueId: space.GetId().GetOpaqueId()},
				},
			},
		},
	})
	if err != nil {
		t.Logf("membership: ListStorageSpaces by id failed: %v", err)
		return
	}
	for _, s := range res.GetStorageSpaces() {
		t.Logf("membership: space=%q owner=%s permissionSet=%+v",
			s.GetName(), s.GetOwner().GetId().GetOpaqueId(), s.GetRootInfo().GetPermissionSet())
		if op := s.GetOpaque(); op != nil {
			for k, v := range op.GetMap() {
				if k == "grants" || k == "groups" || k == "grants_expirations" {
					t.Logf("membership: opaque[%s] = %s", k, string(v.GetValue()))
				} else {
					t.Logf("membership: opaque key present: %s", k)
				}
			}
		}
	}
}

func mustOK(t *testing.T, st *rpc.Status, op string) {
	t.Helper()
	if st.GetCode() != rpc.Code_CODE_OK {
		t.Fatalf("%s failed: code=%s msg=%q", op, st.GetCode(), st.GetMessage())
	}
}

func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

var _ = fmt.Sprintf
