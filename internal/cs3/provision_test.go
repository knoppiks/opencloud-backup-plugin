package cs3

import (
	"context"
	"errors"
	"strings"
	"testing"

	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// fakeProvisioner records what CreateStorageSpace was asked for.
type fakeProvisioner struct {
	req   *provider.CreateStorageSpaceRequest
	token string
	res   *provider.CreateStorageSpaceResponse
	err   error
}

func (f *fakeProvisioner) CreateStorageSpace(ctx context.Context, in *provider.CreateStorageSpaceRequest, _ ...grpc.CallOption) (*provider.CreateStorageSpaceResponse, error) {
	f.req = in
	if md, ok := metadata.FromOutgoingContext(ctx); ok {
		if vals := md.Get(TokenHeader); len(vals) > 0 {
			f.token = vals[0]
		}
	}
	return f.res, f.err
}

// staticAuth is an Authenticator returning a fixed token, or an error.
type staticAuth struct {
	token string
	err   error
}

func (s staticAuth) Token(context.Context) (string, error) { return s.token, s.err }

func okResponse(id string) *provider.CreateStorageSpaceResponse {
	return &provider.CreateStorageSpaceResponse{
		Status: &rpc.Status{Code: rpc.Code_CODE_OK},
		StorageSpace: &provider.StorageSpace{
			Id: &provider.StorageSpaceId{OpaqueId: id},
		},
	}
}

// The Space must be a project Space authenticated as the service account: the
// creator keeps a manager grant OpenCloud will not remove, so creating it as
// anyone else produces a Space cs3state.Check refuses.
func TestCreateProjectSpace(t *testing.T) {
	fake := &fakeProvisioner{res: okResponse("space-1")}

	id, err := CreateProjectSpace(context.Background(), fake, staticAuth{token: "svc-token"}, "Backup service state")
	if err != nil {
		t.Fatalf("CreateProjectSpace: %v", err)
	}
	if id != "space-1" {
		t.Fatalf("id = %q, want space-1", id)
	}
	if got := fake.req.GetType(); got != spaceTypeProject {
		t.Fatalf("space type = %q, want %q", got, spaceTypeProject)
	}
	if got := fake.req.GetName(); got != "Backup service state" {
		t.Fatalf("name = %q", got)
	}
	if fake.token != "svc-token" {
		t.Fatalf("token header = %q, want the authenticator's token", fake.token)
	}
}

func TestCreateProjectSpace_Rejects(t *testing.T) {
	ctx := context.Background()
	ok := &fakeProvisioner{res: okResponse("space-1")}

	cases := map[string]struct {
		gw      SpaceProvisioner
		auth    Authenticator
		name    string
		wantErr string
	}{
		"no gateway":        {gw: nil, auth: staticAuth{token: "t"}, name: "n", wantErr: "gateway is required"},
		"no authenticator":  {gw: ok, auth: nil, name: "n", wantErr: "authenticator is required"},
		"no name":           {gw: ok, auth: staticAuth{token: "t"}, name: "", wantErr: "name is required"},
		"auth fails":        {gw: ok, auth: staticAuth{err: errors.New("no token")}, name: "n", wantErr: "no token"},
		"upstream declines": {gw: &fakeProvisioner{res: &provider.CreateStorageSpaceResponse{Status: &rpc.Status{Code: rpc.Code_CODE_PERMISSION_DENIED, Message: "quota"}}}, auth: staticAuth{token: "t"}, name: "n", wantErr: "PERMISSION_DENIED"},
		"no id returned":    {gw: &fakeProvisioner{res: &provider.CreateStorageSpaceResponse{Status: &rpc.Status{Code: rpc.Code_CODE_OK}}}, auth: staticAuth{token: "t"}, name: "n", wantErr: "no space id"},
		"transport fails":   {gw: &fakeProvisioner{err: errors.New("dial failed")}, auth: staticAuth{token: "t"}, name: "n", wantErr: "dial failed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			id, err := CreateProjectSpace(ctx, tc.gw, tc.auth, tc.name)
			if err == nil {
				t.Fatalf("CreateProjectSpace = %q, want an error", id)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
			}
			if id != "" {
				t.Fatalf("id = %q, want empty on failure", id)
			}
		})
	}
}
