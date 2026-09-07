package main

// Operator commands for retiring a wrapping key.
//
// Both custody keys live in the cluster (decisions.md #1, #14) and can leak
// there. Until these commands existed, a suspected exposure had no remedy: the
// SRW key wraps every Space's Data Key and the TW key wraps every target's
// credentials, and nothing could re-wrap either. Rotation changes the wrapping,
// never the wrapped: Data Keys, snapshots and Recovery Keys are untouched, so
// nothing is re-uploaded and no user has to do anything.
//
// The Recovery Key is deliberately absent from this file. Its plaintext never
// reaches the server, so only the browser can rotate it (see the rotate endpoint
// in pkg/api).

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/rotate"
	"opencloud-backup-plugin/pkg/state"
	"opencloud-backup-plugin/pkg/targets"
)

// rotateTimeout bounds a whole rotation. It visits every Space or target, each a
// small read and a small write, so minutes is generous and an unbounded run
// against an unreachable state Space is not something an operator should have to
// interrupt by hand.
const rotateTimeout = 10 * time.Minute

// runCommand dispatches an operator subcommand.
func runCommand(ctx context.Context, name string, args []string, logger *slog.Logger) error {
	switch name {
	case "rotate-srw":
		return runRotate(ctx, srwRotation, args, logger)
	case "rotate-tw":
		return runRotate(ctx, twRotation, args, logger)
	default:
		return fmt.Errorf("unknown command %q; known commands are rotate-srw and rotate-tw", name)
	}
}

// rotation describes one custody key's rotation: where its keys come from, and
// what re-wrapping means for it.
type rotation struct {
	// name is the subcommand.
	name string
	// oldEnv / newEnv are the environment variables holding the retiring and
	// the incoming key.
	oldEnv, newEnv string
	// what is the human name of the records being re-wrapped.
	what string
	// run performs the rotation against the durable state store.
	run func(ctx context.Context, st state.Store, oldKey, newKey []byte) (rotate.Result, error)
}

var srwRotation = rotation{
	name:   "rotate-srw",
	oldEnv: "SRW_KEY_OLD",
	newEnv: "SRW_KEY",
	what:   "server key envelopes",
	run: func(_ context.Context, st state.Store, oldKey, newKey []byte) (rotate.Result, error) {
		return rotate.SRW(keys.NewStateStore(st, nil), oldKey, newKey)
	},
}

var twRotation = rotation{
	name:   "rotate-tw",
	oldEnv: "TW_KEY_OLD",
	newEnv: "TW_KEY",
	what:   "target credentials",
	run: func(ctx context.Context, st state.Store, oldKey, newKey []byte) (rotate.Result, error) {
		return rotate.TW(ctx, targets.NewStateStore(st), oldKey, newKey)
	},
}

// runRotate re-wraps every affected record from the old key to the new one.
func runRotate(ctx context.Context, r rotation, args []string, logger *slog.Logger) error {
	stopped, err := parseRotateFlags(r, args)
	if err != nil {
		return err
	}
	if !stopped {
		return fmt.Errorf(
			"refusing to rotate while the service may be running: stop it, then pass -service-stopped. "+
				"A run that starts mid-rotation can read an envelope this command has already re-wrapped "+
				"under a key that run does not hold (%s)", r.name)
	}

	oldKey, err := requireWrapKey(r.oldEnv)
	if err != nil {
		return err
	}
	defer keys.Zeroize(oldKey)
	newKey, err := requireWrapKey(r.newEnv)
	if err != nil {
		return err
	}
	defer keys.Zeroize(newKey)

	client, closeCS3, err := dialCS3()
	if err != nil {
		return err
	}
	defer closeCS3()
	if client == nil {
		return errors.New("CS3_GATEWAY_ADDR is required: rotation reads the service's state Space")
	}
	if os.Getenv("STATE_SPACE_ID") == "" {
		return errors.New("STATE_SPACE_ID is required: there is nothing to rotate in in-memory state")
	}
	store, err := buildStateStore(client, logger)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, rotateTimeout)
	defer cancel()

	if err := requireIdle(ctx, store); err != nil {
		return err
	}

	logger.Info("rotating", "command", r.name, "records", r.what)
	result, err := r.run(ctx, store, oldKey, newKey)
	// The counts are reported even on failure: a rotation is resumable, and an
	// operator needs to know how far this attempt got before it stopped.
	logger.Info("rotation finished",
		"command", r.name,
		"rotated", result.Rotated,
		"already_rotated", result.AlreadyRotated,
		"skipped", result.Skipped)
	if err != nil {
		return err
	}
	logger.Warn("remove the old key from the deployment now; it opens nothing any more", "variable", r.oldEnv)
	return nil
}

// parseRotateFlags reads the command's only flag.
func parseRotateFlags(r rotation, args []string) (bool, error) {
	fs := flag.NewFlagSet(r.name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	stopped := fs.Bool("service-stopped", false,
		"confirm the backup service is stopped; rotation must not run alongside backups")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr,
			"usage: backupd %s -service-stopped\n\n"+
				"Re-wraps %s from $%s to $%s. Data keys, snapshots and Recovery Keys\n"+
				"are untouched. Safe to re-run: an interrupted rotation is finished by\n"+
				"running it again.\n\n",
			r.name, r.what, r.oldEnv, r.newEnv)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	if fs.NArg() > 0 {
		return false, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return *stopped, nil
}

// requireWrapKey loads a wrapping key that must be present.
func requireWrapKey(envVar string) ([]byte, error) {
	key, err := loadWrapKey(envVar)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, fmt.Errorf("%s is required", envVar)
	}
	return key, nil
}

// requireIdle refuses to rotate while a run holds a Space.
//
// This is the machine's half of the "-service-stopped" assertion, and it is a
// check rather than a lock: the state backend has no compare-and-set, so a run
// could still start immediately afterwards. It catches the operator who forgot,
// which is the failure that actually happens.
func requireIdle(ctx context.Context, store state.Store) error {
	active, err := jobs.ActiveLeases(ctx, store, time.Now())
	if err != nil {
		return fmt.Errorf("could not check for running backups: %w", err)
	}
	if active > 0 {
		return fmt.Errorf(
			"%d backup run(s) still hold a lease; wait for them to finish or expire, then rotate", active)
	}
	return nil
}
