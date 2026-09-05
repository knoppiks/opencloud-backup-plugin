package cs3

import (
	"context"
	"errors"
	"testing"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	types "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// metadataFromCtx reads outgoing gRPC metadata set via
// metadata.AppendToOutgoingContext (what the client uses to carry the token).
func metadataFromCtx(ctx context.Context) (metadata.MD, bool) {
	return metadata.FromOutgoingContext(ctx)
}

// fakeGateway implements GatewayClient for unit tests.
type fakeGateway struct {
	authToken  string
	authErr    error
	authStatus rpc.Code

	spaces      []*provider.StorageSpace
	spacesErr   error
	spaceStatus rpc.Code

	// dirs maps a reference path ("." or "./sub") to its listed children.
	dirs map[string][]*provider.ResourceInfo
	// listedPaths records every reference path passed to ListContainer.
	listedPaths []string
	// listStatus overrides the ListContainer status code.
	listStatus rpc.Code

	// downloadEndpoint/downloadToken drive InitiateFileDownload responses.
	downloadEndpoint string
	downloadToken    string
	downloadProtocol string
	downloadStatus   rpc.Code
	// downloadedRefPath records the reference path of the last download.
	downloadedRefPath string
	// downloadRootID records the reference resource id of the last download.
	downloadRootID *provider.ResourceId

	// lastTokenSeen records the x-access-token metadata seen on ListStorageSpaces.
	lastTokenSeen string

	// --- write path (restore Path B) ---
	// createdDirs records every reference path passed to CreateContainer.
	createdDirs []string
	// createStatus overrides the CreateContainer status code.
	createStatus rpc.Code
	// uploadEndpoint/uploadToken/uploadProtocol drive InitiateFileUpload.
	uploadEndpoint string
	uploadToken    string
	uploadProtocol string
	uploadStatus   rpc.Code
	// uploadedRefPath records the reference path of the last upload initiation,
	// uploadOpaque its opaque map.
	uploadedRefPath string
	uploadOpaque    map[string]string
}

func okStatus(c rpc.Code) *rpc.Status {
	if c == rpc.Code_CODE_INVALID {
		c = rpc.Code_CODE_OK
	}
	return &rpc.Status{Code: c}
}

func (f *fakeGateway) Authenticate(_ context.Context, _ *gateway.AuthenticateRequest, _ ...grpc.CallOption) (*gateway.AuthenticateResponse, error) {
	if f.authErr != nil {
		return nil, f.authErr
	}
	return &gateway.AuthenticateResponse{
		Status: okStatus(f.authStatus),
		Token:  f.authToken,
		User:   &userpb.User{Id: &userpb.UserId{OpaqueId: "worker"}},
	}, nil
}

func (f *fakeGateway) ListStorageSpaces(ctx context.Context, _ *provider.ListStorageSpacesRequest, _ ...grpc.CallOption) (*provider.ListStorageSpacesResponse, error) {
	if md, ok := metadataFromCtx(ctx); ok {
		if vals := md[TokenHeader]; len(vals) > 0 {
			f.lastTokenSeen = vals[0]
		}
	}
	if f.spacesErr != nil {
		return nil, f.spacesErr
	}
	return &provider.ListStorageSpacesResponse{
		Status:        okStatus(f.spaceStatus),
		StorageSpaces: f.spaces,
	}, nil
}

func (f *fakeGateway) ListContainer(_ context.Context, in *provider.ListContainerRequest, _ ...grpc.CallOption) (*provider.ListContainerResponse, error) {
	p := in.GetRef().GetPath()
	f.listedPaths = append(f.listedPaths, p)
	return &provider.ListContainerResponse{
		Status: okStatus(f.listStatus),
		Infos:  f.dirs[p],
	}, nil
}

func (f *fakeGateway) Stat(context.Context, *provider.StatRequest, ...grpc.CallOption) (*provider.StatResponse, error) {
	return &provider.StatResponse{Status: okStatus(rpc.Code_CODE_OK)}, nil
}

func (f *fakeGateway) InitiateFileDownload(_ context.Context, in *provider.InitiateFileDownloadRequest, _ ...grpc.CallOption) (*gateway.InitiateFileDownloadResponse, error) {
	f.downloadedRefPath = in.GetRef().GetPath()
	f.downloadRootID = in.GetRef().GetResourceId()
	proto := f.downloadProtocol
	if proto == "" {
		proto = "spaces"
	}
	return &gateway.InitiateFileDownloadResponse{
		Status: okStatus(f.downloadStatus),
		Protocols: []*gateway.FileDownloadProtocol{{
			Protocol:         proto,
			DownloadEndpoint: f.downloadEndpoint,
			Token:            f.downloadToken,
		}},
	}, nil
}

func (f *fakeGateway) CreateContainer(_ context.Context, in *provider.CreateContainerRequest, _ ...grpc.CallOption) (*provider.CreateContainerResponse, error) {
	f.createdDirs = append(f.createdDirs, in.GetRef().GetPath())
	return &provider.CreateContainerResponse{Status: okStatus(f.createStatus)}, nil
}

func (f *fakeGateway) InitiateFileUpload(_ context.Context, in *provider.InitiateFileUploadRequest, _ ...grpc.CallOption) (*gateway.InitiateFileUploadResponse, error) {
	f.uploadedRefPath = in.GetRef().GetPath()
	f.uploadOpaque = map[string]string{}
	for k, v := range in.GetOpaque().GetMap() {
		f.uploadOpaque[k] = string(v.GetValue())
	}
	proto := f.uploadProtocol
	if proto == "" {
		proto = "simple"
	}
	return &gateway.InitiateFileUploadResponse{
		Status: okStatus(f.uploadStatus),
		Protocols: []*gateway.FileUploadProtocol{{
			Protocol:       proto,
			UploadEndpoint: f.uploadEndpoint,
			Token:          f.uploadToken,
		}},
	}, nil
}

// dirInfo builds a container ResourceInfo as ListContainer returns it (the CS3
// Path is absolute; only its base name is meaningful to the walker).
func dirInfo(name string) *provider.ResourceInfo {
	return &provider.ResourceInfo{
		Path:  "/" + name,
		Type:  provider.ResourceType_RESOURCE_TYPE_CONTAINER,
		Mtime: &types.Timestamp{Seconds: 100},
	}
}

// fileInfo builds a file ResourceInfo with an explicit size and mtime.
func fileInfo(name string, size uint64, mtime uint64) *provider.ResourceInfo {
	return &provider.ResourceInfo{
		Path:  "/" + name,
		Type:  provider.ResourceType_RESOURCE_TYPE_FILE,
		Size:  size,
		Mtime: &types.Timestamp{Seconds: mtime},
	}
}

func personalSpace(id, name, owner string) *provider.StorageSpace {
	return &provider.StorageSpace{
		Id:        &provider.StorageSpaceId{OpaqueId: id},
		Name:      name,
		SpaceType: "personal",
		Owner:     &userpb.User{Id: &userpb.UserId{OpaqueId: owner}},
	}
}

func projectSpace(id, name string, grantsJSON string) *provider.StorageSpace {
	s := &provider.StorageSpace{
		Id:        &provider.StorageSpaceId{OpaqueId: id},
		Name:      name,
		SpaceType: "project",
	}
	if grantsJSON != "" {
		s.Opaque = &types.Opaque{Map: map[string]*types.OpaqueEntry{
			"grants": {Value: []byte(grantsJSON)},
		}}
	}
	return s
}

func TestListSpaces_MapsFieldsAndForwardsToken(t *testing.T) {
	fg := &fakeGateway{
		authToken: "reva-token-abc",
		spaces: []*provider.StorageSpace{
			personalSpace("s1", "Alice", "alice"),
			projectSpace("s2", "Team", `{"alice":"manager","bob":"editor"}`),
		},
	}
	c := NewClient(fg, ServiceAccountAuth{Gateway: fg, ClientID: "id", Secret: "sec"})

	spaces, err := c.ListSpaces(context.Background())
	if err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	if fg.lastTokenSeen != "reva-token-abc" {
		t.Fatalf("access token not forwarded as %s metadata: got %q", TokenHeader, fg.lastTokenSeen)
	}
	if len(spaces) != 2 {
		t.Fatalf("want 2 spaces, got %d", len(spaces))
	}

	byID := map[string]Space{}
	for _, s := range spaces {
		byID[s.ID] = s
	}
	if byID["s1"].Owner != "alice" || byID["s1"].Type != "personal" || byID["s1"].Members != nil {
		t.Fatalf("personal space mapping wrong: %+v", byID["s1"])
	}
	if byID["s2"].Members["alice"] != "manager" || byID["s2"].Members["bob"] != "editor" {
		t.Fatalf("project membership not parsed from grants: %+v", byID["s2"].Members)
	}
}

func TestListSpaces_AuthError(t *testing.T) {
	fg := &fakeGateway{authErr: errors.New("boom")}
	c := NewClient(fg, ServiceAccountAuth{Gateway: fg, ClientID: "id", Secret: "sec"})
	if _, err := c.ListSpaces(context.Background()); err == nil {
		t.Fatal("expected auth error to propagate")
	}
}

func TestListSpaces_NonOKStatus(t *testing.T) {
	fg := &fakeGateway{authToken: "t", spaceStatus: rpc.Code_CODE_PERMISSION_DENIED}
	c := NewClient(fg, ServiceAccountAuth{Gateway: fg, ClientID: "id", Secret: "sec"})
	if _, err := c.ListSpaces(context.Background()); err == nil {
		t.Fatal("expected non-OK ListStorageSpaces status to error")
	}
}

func TestServiceAccountAuth_EmptyToken(t *testing.T) {
	fg := &fakeGateway{authToken: ""}
	_, err := ServiceAccountAuth{Gateway: fg}.Token(context.Background())
	if err == nil {
		t.Fatal("empty token must error")
	}
}

func TestStaticTokenAuth(t *testing.T) {
	if _, err := (StaticTokenAuth{Value: ""}).Token(context.Background()); err == nil {
		t.Fatal("empty static token must error")
	}
	tok, err := (StaticTokenAuth{Value: "abc"}).Token(context.Background())
	if err != nil || tok != "abc" {
		t.Fatalf("static token: got %q err %v", tok, err)
	}
}

func TestParseMembers_EmptyGrants(t *testing.T) {
	// Personal spaces have grants "{}" or none -> no members.
	if m := parseMembers(projectSpace("s", "n", `{}`)); m != nil {
		t.Fatalf("empty grants should yield nil members, got %+v", m)
	}
	if m := parseMembers(personalSpace("s", "n", "owner")); m != nil {
		t.Fatalf("no opaque should yield nil members, got %+v", m)
	}
}
