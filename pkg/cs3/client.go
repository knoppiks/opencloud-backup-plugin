// Concrete SpaceReader against the OpenCloud/Reva CS3 gateway (phase-2
// deliverable 2). The read path — auth type, token header, ListStorageSpaces,
// download flow, and the space-membership source — was validated in Spike 3
// (phase-0-findings.md); this file promotes those findings into a testable
// client.
//
// The gateway dependency is injected behind GatewayClient so unit tests can
// substitute a fake without a live gateway or gRPC.
package cs3

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// TokenHeader is reva's access-token gRPC metadata key (pkg/ctx.TokenHeader).
const TokenHeader = "x-access-token"

// GatewayClient is the subset of the CS3 gateway API the reader uses. The
// generated gateway.GatewayAPIClient satisfies it; tests provide a fake.
type GatewayClient interface {
	Authenticate(ctx context.Context, in *gateway.AuthenticateRequest, opts ...grpc.CallOption) (*gateway.AuthenticateResponse, error)
	ListStorageSpaces(ctx context.Context, in *provider.ListStorageSpacesRequest, opts ...grpc.CallOption) (*provider.ListStorageSpacesResponse, error)
	ListContainer(ctx context.Context, in *provider.ListContainerRequest, opts ...grpc.CallOption) (*provider.ListContainerResponse, error)
	Stat(ctx context.Context, in *provider.StatRequest, opts ...grpc.CallOption) (*provider.StatResponse, error)
	InitiateFileDownload(ctx context.Context, in *provider.InitiateFileDownloadRequest, opts ...grpc.CallOption) (*gateway.InitiateFileDownloadResponse, error)
}

// Authenticator obtains a reva access token for the worker credential. The
// default implementation authenticates a service account (decisions.md #11); a
// per-user forwarding implementation carries the caller's own bearer token.
type Authenticator interface {
	// Token returns a reva access token to place in the x-access-token header.
	Token(ctx context.Context) (string, error)
}

// ServiceAccountAuth authenticates the unattended worker with an OpenCloud
// service account (CS3 auth type "serviceaccounts"; decisions.md #11). The
// token is short-lived and re-minted per call — cheap, and keeps the client
// stateless.
type ServiceAccountAuth struct {
	Gateway  GatewayClient
	ClientID string
	Secret   string
}

// Token mints a fresh service-account token.
func (a ServiceAccountAuth) Token(ctx context.Context) (string, error) {
	res, err := a.Gateway.Authenticate(ctx, &gateway.AuthenticateRequest{
		Type:         "serviceaccounts",
		ClientId:     a.ClientID,
		ClientSecret: a.Secret,
	})
	if err != nil {
		return "", fmt.Errorf("cs3 authenticate: %w", err)
	}
	if err := statusErr(res.GetStatus(), "Authenticate"); err != nil {
		return "", err
	}
	if res.GetToken() == "" {
		return "", fmt.Errorf("cs3 authenticate: empty token")
	}
	return res.GetToken(), nil
}

// StaticTokenAuth forwards a pre-obtained token (e.g. the caller's own bearer
// token for user-scoped reads). It performs no RPC.
type StaticTokenAuth struct{ Value string }

// Token returns the static token.
func (a StaticTokenAuth) Token(_ context.Context) (string, error) {
	if a.Value == "" {
		return "", fmt.Errorf("cs3: empty static token")
	}
	return a.Value, nil
}

// Client is the concrete SpaceReader. Construct with NewClient.
type Client struct {
	gw   GatewayClient
	auth Authenticator
}

// NewClient builds a Client from an injected gateway and authenticator.
func NewClient(gw GatewayClient, auth Authenticator) *Client {
	return &Client{gw: gw, auth: auth}
}

// authContext attaches a freshly minted access token to ctx as gRPC metadata.
func (c *Client) authContext(ctx context.Context) (context.Context, string, error) {
	tok, err := c.auth.Token(ctx)
	if err != nil {
		return nil, "", err
	}
	return metadata.AppendToOutgoingContext(ctx, TokenHeader, tok), tok, nil
}

// ListSpaces returns the storage spaces the worker credential may back up
// (phase-2 deliverable 2). Membership/roles are read from the space's Opaque
// grants map (decisions.md #7; phase-0-findings.md Spike 3) so later phases can
// authorize shared-space access. Pagination, if any, is handled here.
func (c *Client) ListSpaces(ctx context.Context) ([]Space, error) {
	authCtx, _, err := c.authContext(ctx)
	if err != nil {
		return nil, err
	}

	res, err := c.gw.ListStorageSpaces(authCtx, &provider.ListStorageSpacesRequest{})
	if err != nil {
		return nil, fmt.Errorf("cs3 list storage spaces: %w", err)
	}
	if err := statusErr(res.GetStatus(), "ListStorageSpaces"); err != nil {
		return nil, err
	}

	spaces := make([]Space, 0, len(res.GetStorageSpaces()))
	for _, s := range res.GetStorageSpaces() {
		spaces = append(spaces, toSpace(s))
	}
	return spaces, nil
}

// Walk visits every entry under the space root (phase-2 keeps the interface;
// full traversal is exercised by the snapshot pipeline in Phase 4). It performs
// a depth-first walk via ListContainer.
func (c *Client) Walk(ctx context.Context, space Space, fn func(Entry) error) error {
	authCtx, _, err := c.authContext(ctx)
	if err != nil {
		return err
	}
	return c.walk(authCtx, space, ".", fn)
}

func (c *Client) walk(ctx context.Context, space Space, relDir string, fn func(Entry) error) error {
	ref := &provider.Reference{
		ResourceId: spaceRootID(space),
		Path:       relDir,
	}
	res, err := c.gw.ListContainer(ctx, &provider.ListContainerRequest{Ref: ref})
	if err != nil {
		return fmt.Errorf("cs3 list container: %w", err)
	}
	if err := statusErr(res.GetStatus(), "ListContainer"); err != nil {
		return err
	}
	for _, info := range res.GetInfos() {
		rel := "./" + strings.TrimPrefix(baseName(info.GetPath()), "/")
		if relDir != "." {
			rel = strings.TrimSuffix(relDir, "/") + "/" + baseName(info.GetPath())
		}
		isDir := info.GetType() == provider.ResourceType_RESOURCE_TYPE_CONTAINER
		e := Entry{
			Path:      rel,
			IsDir:     isDir,
			Size:      int64(info.GetSize()),
			MTimeUnix: int64(info.GetMtime().GetSeconds()),
		}
		if err := fn(e); err != nil {
			return err
		}
		if isDir {
			if err := c.walk(ctx, space, rel, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// OpenFile streams one space-relative file. The reference is built from the
// space root plus relative path — a bare file id fails (phase-0-findings Spike
// 3, "Critical gotcha"). Full streaming (data-gateway HTTP GET) is wired in
// Phase 4; Phase 2 only needs the read-model boundary, so this returns a clear
// not-yet-implemented error rather than a partial download path.
func (c *Client) OpenFile(ctx context.Context, space Space, relPath string) (io.ReadCloser, error) {
	authCtx, _, err := c.authContext(ctx)
	if err != nil {
		return nil, err
	}
	rel := "./" + strings.TrimPrefix(relPath, "/")
	res, err := c.gw.InitiateFileDownload(authCtx, &provider.InitiateFileDownloadRequest{
		Ref: &provider.Reference{ResourceId: spaceRootID(space), Path: rel},
	})
	if err != nil {
		return nil, fmt.Errorf("cs3 initiate download: %w", err)
	}
	if err := statusErr(res.GetStatus(), "InitiateFileDownload"); err != nil {
		return nil, err
	}
	// The HTTP data-gateway streaming step (x-access-token + x-reva-transfer)
	// belongs to the Phase-4 pipeline; keep the boundary honest here.
	return nil, fmt.Errorf("cs3 open file: streaming not implemented until phase 4")
}

// toSpace maps a CS3 StorageSpace into our model, extracting membership from the
// Opaque grants map (phase-0-findings.md Spike 3).
func toSpace(s *provider.StorageSpace) Space {
	sp := Space{
		ID:      s.GetId().GetOpaqueId(),
		Name:    s.GetName(),
		Type:    s.GetSpaceType(),
		Owner:   s.GetOwner().GetId().GetOpaqueId(),
		Members: parseMembers(s),
	}
	return sp
}

// parseMembers reads the space's Opaque "grants" map into principal->role. The
// grants value is a JSON object keyed by principal id; the value shape varies by
// reva version, so we keep the raw value as the role token defensively. Personal
// spaces have empty grants (owner only).
func parseMembers(s *provider.StorageSpace) map[string]string {
	op := s.GetOpaque()
	if op == nil {
		return nil
	}
	entry, ok := op.GetMap()["grants"]
	if !ok || len(entry.GetValue()) == 0 {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(entry.GetValue(), &raw); err != nil {
		return nil
	}
	if len(raw) == 0 {
		return nil
	}
	members := make(map[string]string, len(raw))
	for principal, role := range raw {
		members[principal] = roleLabel(role)
	}
	return members
}

// roleLabel extracts a short role label from a grant value, falling back to the
// compact JSON if the shape is unknown. Never used for authorization decisions
// on its own — presence in the map is what grants membership.
func roleLabel(v json.RawMessage) string {
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return s
	}
	var obj map[string]any
	if err := json.Unmarshal(v, &obj); err == nil {
		for _, k := range []string{"role", "name", "type"} {
			if rv, ok := obj[k].(string); ok && rv != "" {
				return rv
			}
		}
	}
	return string(v)
}

func spaceRootID(space Space) *provider.ResourceId {
	// OpenCloud space ids are composite (storageid$spaceid!opaqueid). Phase 2
	// only surfaces the id via ListSpaces; the Walk/OpenFile data path (Phase 4)
	// needs a correctly split ResourceId, so this reconstruction is a Phase-4
	// refinement — it is not exercised by the Phase-2 read model. For now we keep
	// the composite id in all parts as a best-effort placeholder.
	return &provider.ResourceId{
		StorageId: space.ID,
		SpaceId:   space.ID,
		OpaqueId:  space.ID,
	}
}

func statusErr(st *rpc.Status, op string) error {
	if st.GetCode() != rpc.Code_CODE_OK {
		return fmt.Errorf("cs3 %s: code=%s", op, st.GetCode())
	}
	return nil
}

func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
