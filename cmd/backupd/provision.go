package main

// Operator command: create the service's state Space.
//
// This exists because the state Space cannot be created through OpenCloud's
// admin UI. The README used to say "create a project Space and add no members
// to it"; on OpenCloud 7.3.0 that is impossible — the creator is given a manager
// grant and removing the last one is refused outright. The Space the service
// will accept is one created *by the service account*, whose only grant is the
// service account's own (see pkg/cs3/provision.go for the measurements, and
// cs3state.Check for how that one grant is discounted).
//
// It is an operator command rather than something startup does implicitly. A
// service that provisions its own storage on boot would, on a misconfigured
// STATE_SPACE_ID, quietly create a second Space and start writing to it — and
// the first one, holding every wrapped Data Key, would still be sitting there
// unreferenced.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/cs3state"
)

// provisionTimeout bounds the command. It is one gateway call plus a listing.
const provisionTimeout = 2 * time.Minute

// defaultStateSpaceName is what the Space is called when the operator does not
// choose. It is visible to nobody but the service account, so the only audience
// is an operator reading a space list and wondering what it is.
const defaultStateSpaceName = "Backup service state"

// runProvisionStateSpace creates a state Space and prints its id.
func runProvisionStateSpace(
	ctx context.Context, cs3Env cs3.Env, st stateEnv, args []string, logger *slog.Logger, stdout io.Writer,
) error {
	name, err := parseProvisionFlags(args)
	if err != nil {
		return err
	}

	// The Space must be created by the service account, because whoever
	// creates it keeps the one grant OpenCloud will not let anyone remove.
	// cs3.Env.Validate refuses a gateway without the service account.
	addr := cs3Env.GatewayAddr
	if addr == "" {
		return errors.New("CS3_GATEWAY_ADDR is required: the state Space is created over CS3")
	}
	saID := cs3Env.ServiceAccountID.Reveal()
	saSecret := cs3Env.ServiceAccountSecret.Reveal()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	gw := gateway.NewGatewayAPIClient(conn)
	auth := cs3.NewCachedAuth(cs3.ServiceAccountAuth{Gateway: gw, ClientID: saID, Secret: saSecret})
	opts, err := cs3ClientOptions(cs3Env)
	if err != nil {
		return err
	}
	client := cs3.NewClient(gw, auth, opts...)

	ctx, cancel := context.WithTimeout(ctx, provisionTimeout)
	defer cancel()

	// Refusing to make a second one is the point of the check. Every wrapped
	// Data Key the service holds lives in the Space it is already using, and a
	// duplicate is how an operator ends up with two and no idea which.
	if st.SpaceID != "" {
		return fmt.Errorf(
			"STATE_SPACE_ID is already set: unset it to provision a new state Space, or leave it " +
				"alone. Creating a second one would leave the first holding every wrapped Data Key " +
				"with nothing pointing at it")
	}

	return provisionStateSpace(ctx, name, cs3SpaceProvisioner{
		create: func(ctx context.Context, name string) (string, error) {
			return cs3.CreateProjectSpace(ctx, gw, auth, name)
		},
		check: func(ctx context.Context, id string) error {
			store, err := cs3state.New(client, cs3state.Options{
				SpaceID:          id,
				Prefix:           st.Prefix,
				ServiceAccountID: saID,
			})
			if err != nil {
				return err
			}
			return store.Check(ctx)
		},
	}, logger, stdout)
}

// spaceProvisioner creates a state Space and checks it the way startup will.
type spaceProvisioner interface {
	Create(ctx context.Context, name string) (string, error)
	Check(ctx context.Context, id string) error
}

// cs3SpaceProvisioner is the spaceProvisioner over a live CS3 gateway.
type cs3SpaceProvisioner struct {
	create func(ctx context.Context, name string) (string, error)
	check  func(ctx context.Context, id string) error
}

func (p cs3SpaceProvisioner) Create(ctx context.Context, name string) (string, error) {
	return p.create(ctx, name)
}

func (p cs3SpaceProvisioner) Check(ctx context.Context, id string) error { return p.check(ctx, id) }

// provisionStateSpace creates the Space, verifies it, and prints its id.
func provisionStateSpace(
	ctx context.Context, name string, p spaceProvisioner, logger *slog.Logger, stdout io.Writer,
) error {
	id, err := p.Create(ctx, name)
	if err != nil {
		return err
	}

	// Verify with the same predicate startup uses, rather than trusting that
	// creation did what this file claims. If OpenCloud ever changes who gets
	// the initial grant, the operator finds out here and not at the next
	// restart.
	if err := p.Check(ctx, id); err != nil {
		return fmt.Errorf("the Space was created (id %s) but is not usable as service state: %w", id, err)
	}

	logger.Info("state space created", "name", name, "space", id)
	// stdout, unadorned: this is the one value the operator has to copy into
	// their deployment, and it should survive a pipe into a variable. The log
	// line above goes to stderr (newLogger), never here.
	_, err = fmt.Fprintln(stdout, id)
	return err
}

// parseProvisionFlags reads the command's only flag.
func parseProvisionFlags(args []string) (string, error) {
	fs := flag.NewFlagSet("provision-state-space", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	name := fs.String("name", defaultStateSpaceName, "name of the Space to create")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr,
			"usage: backupd provision-state-space [-name NAME]\n\n"+
				"Creates the project Space the service keeps its state in, as the service\n"+
				"account, and prints its id on stdout for STATE_SPACE_ID.\n\n"+
				"This cannot be done from the OpenCloud admin UI: a Space created there\n"+
				"leaves its creator holding a manager grant, and OpenCloud refuses to remove\n"+
				"the last one, so the Space would be one an end user can empty. A Space\n"+
				"created here is granted to the service account alone.\n\n"+
				"Needs CS3_GATEWAY_ADDR and the service account credentials, and refuses to\n"+
				"run while STATE_SPACE_ID is already set.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() > 0 {
		return "", fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *name == "" {
		return "", errors.New("-name must not be empty")
	}
	return *name, nil
}
