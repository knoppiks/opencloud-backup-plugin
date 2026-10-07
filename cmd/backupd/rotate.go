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
// in internal/api).

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/knoppiks/opencloud-backup-plugin/internal/cli"
	"github.com/knoppiks/opencloud-backup-plugin/internal/config"
	"github.com/knoppiks/opencloud-backup-plugin/internal/jobs"
	"github.com/knoppiks/opencloud-backup-plugin/internal/rotate"
	"github.com/knoppiks/opencloud-backup-plugin/internal/state"
	"github.com/knoppiks/opencloud-backup-plugin/internal/targets"
	"github.com/knoppiks/opencloud-backup-plugin/pkg/keys"
)

// rotateTimeout bounds a whole rotation. It visits every Space or target, each a
// small read and a small write, so minutes is generous and an unbounded run
// against an unreachable state Space is not something an operator should have to
// interrupt by hand.
const rotateTimeout = 10 * time.Minute

// rotation describes one custody key's rotation: where its keys come from, and
// what re-wrapping means for it.
type rotation struct {
	// name is the subcommand.
	name string
	// oldEnv / newEnv are the environment variables holding the retiring and
	// the incoming key.
	oldEnv, newEnv string
	// keys picks the retiring and the incoming key from the configuration.
	keys func(config.Keys) (oldKey, newKey config.Secret)
	// what is the human name of the records being re-wrapped.
	what string
	// run performs the rotation against the durable state store.
	run func(ctx context.Context, st state.Store, oldKey, newKey []byte) (rotate.Result, error)
}

var srwRotation = rotation{
	name:   "rotate-srw",
	oldEnv: "SRW_KEY_OLD",
	newEnv: "SRW_KEY",
	keys:   func(k config.Keys) (config.Secret, config.Secret) { return k.SRWOld, k.SRW },
	what:   "server key envelopes",
	run: func(ctx context.Context, st state.Store, oldKey, newKey []byte) (rotate.Result, error) {
		return rotate.SRW(ctx, keys.NewStateStore(st, nil), oldKey, newKey)
	},
}

var twRotation = rotation{
	name:   "rotate-tw",
	oldEnv: "TW_KEY_OLD",
	newEnv: "TW_KEY",
	keys:   func(k config.Keys) (config.Secret, config.Secret) { return k.TWOld, k.TW },
	what:   "target credentials",
	run: func(ctx context.Context, st state.Store, oldKey, newKey []byte) (rotate.Result, error) {
		return rotate.TW(ctx, targets.NewStateStore(st), oldKey, newKey)
	},
}

// parseRotate reads a rotation's command line. The operator's assertion is
// required before anything is read, let alone written: a rotation racing a
// backup run hands that run an envelope it cannot open.
func parseRotate(r rotation) func(args []string, stdout, stderr io.Writer) (action, error) {
	return func(args []string, stdout, stderr io.Writer) (action, error) {
		fs := flag.NewFlagSet(r.name, flag.ContinueOnError)
		stopped := fs.Bool("service-stopped", false,
			"confirm the backup service is stopped; rotation must not run alongside backups")
		fs.Usage = func() {
			_, _ = fmt.Fprintf(fs.Output(),
				"usage: backupd %s -service-stopped\n\n"+
					"Re-wraps %s from $%s to $%s. Data keys, snapshots and Recovery Keys\n"+
					"are untouched. Safe to re-run: an interrupted rotation is finished by\n"+
					"running it again.\n\n",
				r.name, r.what, r.oldEnv, r.newEnv)
			fs.PrintDefaults()
		}
		if err := cli.Parse(fs, args, stdout, stderr); err != nil {
			return nil, err
		}
		if !*stopped {
			return nil, cli.Usagef(
				"refusing to rotate while the service may be running: stop it, then pass -service-stopped. "+
					"A run that starts mid-rotation can read an envelope this command has already re-wrapped "+
					"under a key that run does not hold (%s)", r.name)
		}
		return func(ctx context.Context, cfg config.Backupd, logger *slog.Logger, _ io.Writer) error {
			return runRotate(ctx, cfg, r, logger)
		}, nil
	}
}

// runRotate re-wraps every affected record from the old key to the new one.
func runRotate(ctx context.Context, cfg config.Backupd, r rotation, logger *slog.Logger) error {
	oldSecret, newSecret := r.keys(cfg.Keys)
	oldKey, err := requireWrapKey(r.oldEnv, oldSecret)
	if err != nil {
		return err
	}
	defer keys.Zeroize(oldKey)
	newKey, err := requireWrapKey(r.newEnv, newSecret)
	if err != nil {
		return err
	}
	defer keys.Zeroize(newKey)

	client, closeCS3, err := dialCS3(cfg.CS3)
	if err != nil {
		return err
	}
	defer closeCS3()
	if client == nil {
		return errors.New("CS3_GATEWAY_ADDR is required: rotation reads the service's state Space")
	}
	if cfg.State.SpaceID == "" {
		return errors.New("STATE_SPACE_ID is required: there is nothing to rotate in in-memory state")
	}
	store, err := buildStateStore(cfg, client, logger)
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

// requireWrapKey loads a wrapping key that must be present.
func requireWrapKey(envVar string, s config.Secret) ([]byte, error) {
	key, err := config.DecodeWrapKey(envVar, s)
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
