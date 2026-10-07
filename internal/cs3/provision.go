package cs3

// Provisioning the service's own state Space.
//
// This is not part of the run path; it exists because the state Space cannot be
// created through OpenCloud's admin UI or graph API in a form the service will
// accept. Measured against OpenCloud 7.3.0:
//
//   - A project Space created through graph leaves its *creator* — a real admin
//     user — holding a manager grant.
//   - That grant cannot be removed: graph answers 403 `accessDenied`, "cannot
//     remove the last share with manager permissions on a space root".
//   - So a Space with zero member grants cannot be produced at all, and the
//     operator instruction "create a project Space and add no members" was
//     impossible to follow.
//
// A Space created over CS3 *by the service account* has exactly one grant, held
// by the service account itself. No end user can reach it, which is the property
// decisions.md #16 asks for; cs3state.Check knows to discount that one
// principal. This file is how such a Space comes into being.

import (
	"context"
	"fmt"

	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// spaceTypeProject is the CS3 space type of a shared, non-personal Space.
const spaceTypeProject = "project"

// SpaceProvisioner is the single gateway call provisioning needs.
//
// Deliberately not folded into GatewayClient: that interface is the service's
// run path, implemented by fakes throughout the test suite, and none of them
// will ever create a Space. R9's lesson was that an interface method nothing
// calls costs every fake that has to implement it.
type SpaceProvisioner interface {
	CreateStorageSpace(ctx context.Context, in *provider.CreateStorageSpaceRequest, opts ...grpc.CallOption) (*provider.CreateStorageSpaceResponse, error)
}

// CreateProjectSpace creates a project Space owned by the authenticated
// principal and returns its id.
//
// The caller must be the service account: whoever creates the Space ends up
// holding the grant that cannot be removed, so creating it as anyone else
// produces a Space the service will refuse to use.
func CreateProjectSpace(ctx context.Context, gw SpaceProvisioner, auth Authenticator, name string) (string, error) {
	if gw == nil {
		return "", fmt.Errorf("cs3: gateway is required to create a space")
	}
	if auth == nil {
		return "", fmt.Errorf("cs3: authenticator is required to create a space")
	}
	if name == "" {
		return "", fmt.Errorf("cs3: space name is required")
	}

	tok, err := auth.Token(ctx)
	if err != nil {
		return "", err
	}
	authCtx := metadata.AppendToOutgoingContext(ctx, TokenHeader, tok)

	res, err := gw.CreateStorageSpace(authCtx, &provider.CreateStorageSpaceRequest{
		Type: spaceTypeProject,
		Name: name,
	})
	if err != nil {
		return "", fmt.Errorf("cs3 create storage space: %w", err)
	}
	if code := res.GetStatus().GetCode(); code != rpc.Code_CODE_OK {
		// The upstream message is included: this runs in an operator's
		// terminal, not in a response to a user, and "it failed" is not
		// actionable when the cause is a quota or a permission.
		return "", fmt.Errorf("cs3 create storage space: %s: %s", code, res.GetStatus().GetMessage())
	}

	id := res.GetStorageSpace().GetId().GetOpaqueId()
	if id == "" {
		return "", fmt.Errorf("cs3 create storage space: OpenCloud returned no space id")
	}
	return id, nil
}
