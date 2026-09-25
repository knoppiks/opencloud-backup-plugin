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
func runProvisionStateSpace(ctx context.Context, args []string, logger *slog.Logger) error {
	name, err := parseProvisionFlags(args)
	if err != nil {
		return err
	}

	addr := os.Getenv("CS3_GATEWAY_ADDR")
	if addr == "" {
		return errors.New("CS3_GATEWAY_ADDR is required: the state Space is created over CS3")
	}
	saID := os.Getenv("OC_SERVICE_ACCOUNT_ID")
	saSecret := os.Getenv("OC_SERVICE_ACCOUNT_SECRET")
	if saID == "" || saSecret == "" {
		return errors.New(
			"OC_SERVICE_ACCOUNT_ID and OC_SERVICE_ACCOUNT_SECRET are required: the Space must be " +
				"created by the service account, because whoever creates it keeps the one grant " +
				"OpenCloud will not let anyone remove")
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	gw := gateway.NewGatewayAPIClient(conn)
	auth := cs3.NewCachedAuth(cs3.ServiceAccountAuth{Gateway: gw, ClientID: saID, Secret: saSecret})
	client := cs3.NewClient(gw, auth, cs3.WithHTTPClient(dataGatewayClient()))

	ctx, cancel := context.WithTimeout(ctx, provisionTimeout)
	defer cancel()

	// Refusing to make a second one is the point of the check. Every wrapped
	// Data Key the service holds lives in the Space it is already using, and a
	// duplicate is how an operator ends up with two and no idea which.
	if existing := os.Getenv("STATE_SPACE_ID"); existing != "" {
		return fmt.Errorf(
			"STATE_SPACE_ID is already set: unset it to provision a new state Space, or leave it " +
				"alone. Creating a second one would leave the first holding every wrapped Data Key " +
				"with nothing pointing at it")
	}

	id, err := cs3.CreateProjectSpace(ctx, gw, auth, name)
	if err != nil {
		return err
	}

	// Verify with the same predicate startup uses, rather than trusting that
	// creation did what this file claims. If OpenCloud ever changes who gets
	// the initial grant, the operator finds out here and not at the next
	// restart.
	store, err := cs3state.New(client, cs3state.Options{
		SpaceID:          id,
		Prefix:           envOr("STATE_PREFIX", cs3state.DefaultPrefix),
		ServiceAccountID: saID,
	})
	if err != nil {
		return err
	}
	if err := store.Check(ctx); err != nil {
		return fmt.Errorf("the Space was created (id %s) but is not usable as service state: %w", id, err)
	}

	logger.Info("state space created", "name", name, "space", id)
	// stdout, unadorned: this is the one value the operator has to copy into
	// their deployment, and it should survive a pipe into a variable.
	fmt.Println(id)
	return nil
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
