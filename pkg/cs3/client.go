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
	"net/http"
	"path"
	"strings"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const (
	// TokenHeader is reva's access-token gRPC metadata key (pkg/ctx.TokenHeader).
	// The same header name is used on the data-gateway HTTP request.
	TokenHeader = "x-access-token"
	// TransferHeader carries the per-download transfer token returned by
	// InitiateFileDownload (phase-0-findings.md Spike 3, step 7).
	TransferHeader = "X-Reva-Transfer"
	// preferredDownloadProtocol is the protocol name OpenCloud returns for
	// spaces-based downloads; any other protocol is accepted as a fallback.
	preferredDownloadProtocol = "spaces"
)

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
	http *http.Client
}

var _ SpaceReader = (*Client)(nil)

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithHTTPClient sets the HTTP client used to stream file bytes from the reva
// data gateway. Injected so deployments can supply their own TLS/proxy config
// and tests can point at an httptest server.
func WithHTTPClient(h *http.Client) ClientOption {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// NewClient builds a Client from an injected gateway and authenticator.
func NewClient(gw GatewayClient, auth Authenticator, opts ...ClientOption) *Client {
	c := &Client{gw: gw, auth: auth, http: http.DefaultClient}
	for _, opt := range opts {
		opt(c)
	}
	return c
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

// ListDir returns the direct children of one space-relative directory via
// ListContainer. relDir is space-relative with no leading "./"; the empty string
// denotes the space root.
func (c *Client) ListDir(ctx context.Context, space Space, relDir string) ([]Entry, error) {
	authCtx, _, err := c.authContext(ctx)
	if err != nil {
		return nil, err
	}
	return c.listDir(authCtx, space, cleanRel(relDir))
}

// listDir performs the ListContainer call on an already-authenticated context.
func (c *Client) listDir(ctx context.Context, space Space, relDir string) ([]Entry, error) {
	res, err := c.gw.ListContainer(ctx, &provider.ListContainerRequest{
		Ref: reference(space, relDir),
	})
	if err != nil {
		return nil, fmt.Errorf("cs3 list container: %w", err)
	}
	if err := statusErr(res.GetStatus(), "ListContainer"); err != nil {
		return nil, err
	}

	infos := res.GetInfos()
	entries := make([]Entry, 0, len(infos))
	for _, info := range infos {
		name := baseName(info.GetPath())
		if name == "" || name == "." {
			continue
		}
		entries = append(entries, Entry{
			Path:      path.Join(relDir, name),
			IsDir:     info.GetType() == provider.ResourceType_RESOURCE_TYPE_CONTAINER,
			Size:      int64(info.GetSize()),
			MTimeUnix: int64(info.GetMtime().GetSeconds()),
		})
	}
	return entries, nil
}

// Walk visits every entry under the space root depth-first, calling fn for each.
// A directory is reported before its children.
func (c *Client) Walk(ctx context.Context, space Space, fn func(Entry) error) error {
	authCtx, _, err := c.authContext(ctx)
	if err != nil {
		return err
	}
	return c.walk(authCtx, space, "", fn)
}

func (c *Client) walk(ctx context.Context, space Space, relDir string, fn func(Entry) error) error {
	entries, err := c.listDir(ctx, space, relDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := fn(e); err != nil {
			return err
		}
		if e.IsDir {
			if err := c.walk(ctx, space, e.Path, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// OpenFile streams one space-relative file starting at offset. The CS3 reference
// is built from the space root plus relative path — a bare file id fails
// (phase-0-findings.md Spike 3, "Critical gotcha").
//
// InitiateFileDownload yields a data-gateway URL plus a transfer token; the
// bytes are then fetched over HTTP carrying both the worker access token and the
// transfer token (Spike 3, steps 6–7). A non-zero offset is requested with a
// Range header and, if the gateway ignores it, satisfied by discarding the
// leading bytes so callers always observe the requested position.
func (c *Client) OpenFile(ctx context.Context, space Space, relPath string, offset int64) (io.ReadCloser, error) {
	if offset < 0 {
		return nil, fmt.Errorf("cs3 open file: negative offset")
	}
	rel := cleanRel(relPath)
	if rel == "" {
		return nil, fmt.Errorf("cs3 open file: empty path")
	}

	authCtx, token, err := c.authContext(ctx)
	if err != nil {
		return nil, err
	}
	res, err := c.gw.InitiateFileDownload(authCtx, &provider.InitiateFileDownloadRequest{
		Ref: reference(space, rel),
	})
	if err != nil {
		return nil, fmt.Errorf("cs3 initiate download: %w", err)
	}
	if err := statusErr(res.GetStatus(), "InitiateFileDownload"); err != nil {
		return nil, err
	}
	endpoint, transfer := pickDownloadProtocol(res.GetProtocols())
	if endpoint == "" {
		return nil, fmt.Errorf("cs3 initiate download: no download endpoint returned")
	}

	return c.stream(ctx, endpoint, token, transfer, offset)
}

// stream performs the data-gateway GET and returns the body positioned at
// offset. It never includes tokens in error messages.
func (c *Client) stream(ctx context.Context, endpoint, accessToken, transferToken string, offset int64) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("cs3 download request: %w", err)
	}
	req.Header.Set(TokenHeader, accessToken)
	if transferToken != "" {
		req.Header.Set(TransferHeader, transferToken)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cs3 download: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		// Server honoured the range; body already starts at offset.
		return resp.Body, nil
	case http.StatusOK:
		if offset == 0 {
			return resp.Body, nil
		}
		// Range ignored: skip forward so the caller's contract still holds.
		if _, err := io.CopyN(io.Discard, resp.Body, offset); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("cs3 download: seek to offset %d: %w", offset, err)
		}
		return resp.Body, nil
	default:
		_ = resp.Body.Close()
		// Status only — the response body may echo internal detail.
		return nil, fmt.Errorf("cs3 download: unexpected status %d", resp.StatusCode)
	}
}

// pickDownloadProtocol selects the spaces protocol when offered, else the first
// protocol advertising an endpoint.
func pickDownloadProtocol(protocols []*gateway.FileDownloadProtocol) (endpoint, transferToken string) {
	for _, p := range protocols {
		if p.GetDownloadEndpoint() == "" {
			continue
		}
		if p.GetProtocol() == preferredDownloadProtocol {
			return p.GetDownloadEndpoint(), p.GetToken()
		}
		if endpoint == "" {
			endpoint, transferToken = p.GetDownloadEndpoint(), p.GetToken()
		}
	}
	return endpoint, transferToken
}

// toSpace maps a CS3 StorageSpace into our model, extracting membership from the
// Opaque grants map (phase-0-findings.md Spike 3).
func toSpace(s *provider.StorageSpace) Space {
	sp := Space{
		ID:      s.GetId().GetOpaqueId(),
		Name:    s.GetName(),
		Type:    s.GetSpaceType(),
		Owner:   s.GetOwner().GetId().GetOpaqueId(),
		Root:    toResourceID(s.GetRoot()),
		Members: parseMembers(s),
	}
	return sp
}

// toResourceID copies the space root triple verbatim.
func toResourceID(id *provider.ResourceId) ResourceID {
	return ResourceID{
		StorageID: id.GetStorageId(),
		SpaceID:   id.GetSpaceId(),
		OpaqueID:  id.GetOpaqueId(),
	}
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

// reference builds the space-relative CS3 reference the data path requires: the
// space root resource id plus a reva-style relative path ("." for the root,
// "./sub/file" otherwise). A bare resource-id reference resolves to "/" on the
// data server and fails (phase-0-findings.md Spike 3, "Critical gotcha").
func reference(space Space, rel string) *provider.Reference {
	p := "."
	if rel != "" {
		p = "./" + rel
	}
	return &provider.Reference{
		ResourceId: spaceRootID(space),
		Path:       p,
	}
}

// spaceRootID returns the space root resource id. OpenCloud space ids are
// composite (storageid$spaceid[!opaqueid]); when the gateway did not surface a
// root triple we split the composite id rather than guessing.
func spaceRootID(space Space) *provider.ResourceId {
	if !space.Root.Zero() {
		return &provider.ResourceId{
			StorageId: space.Root.StorageID,
			SpaceId:   space.Root.SpaceID,
			OpaqueId:  space.Root.OpaqueID,
		}
	}
	storageID, spaceID, opaqueID := splitSpaceID(space.ID)
	return &provider.ResourceId{StorageId: storageID, SpaceId: spaceID, OpaqueId: opaqueID}
}

// splitSpaceID decomposes "storageid$spaceid!opaqueid". Missing components fall
// back to the space id itself, which is what a single-segment id means.
func splitSpaceID(id string) (storageID, spaceID, opaqueID string) {
	storageID, spaceID = id, id
	if i := strings.Index(id, "$"); i >= 0 {
		storageID, spaceID = id[:i], id[i+1:]
	}
	opaqueID = spaceID
	if i := strings.Index(spaceID, "!"); i >= 0 {
		spaceID, opaqueID = spaceID[:i], spaceID[i+1:]
	}
	return storageID, spaceID, opaqueID
}

// cleanRel normalises a space-relative path to slash-separated form with no
// leading "./" or "/" and no trailing slash. The empty string means the root.
func cleanRel(rel string) string {
	rel = strings.TrimPrefix(rel, "./")
	rel = strings.Trim(rel, "/")
	if rel == "" || rel == "." {
		return ""
	}
	return path.Clean(rel)
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
