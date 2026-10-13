package main

// The parts of buildService that are wired as a unit: authentication, key
// custody, and the backup, restore and scheduling pipelines. Split out of
// buildService to keep it readable (gocognit, #84); the order and the
// conditions are unchanged.

import (
	"context"
	"log/slog"
	"time"

	"github.com/knoppiks/opencloud-backup-plugin/internal/api"
	"github.com/knoppiks/opencloud-backup-plugin/internal/backup"
	"github.com/knoppiks/opencloud-backup-plugin/internal/cs3"
	"github.com/knoppiks/opencloud-backup-plugin/internal/jobs"
	"github.com/knoppiks/opencloud-backup-plugin/internal/keystore"
	"github.com/knoppiks/opencloud-backup-plugin/internal/notify"
	"github.com/knoppiks/opencloud-backup-plugin/internal/objstore"
	"github.com/knoppiks/opencloud-backup-plugin/internal/restore"
	"github.com/knoppiks/opencloud-backup-plugin/internal/scheduler"
	"github.com/knoppiks/opencloud-backup-plugin/internal/snapshot"
	"github.com/knoppiks/opencloud-backup-plugin/internal/snapshot/s3repo"
	"github.com/knoppiks/opencloud-backup-plugin/internal/spacecfg"
	"github.com/knoppiks/opencloud-backup-plugin/internal/state"
	"github.com/knoppiks/opencloud-backup-plugin/internal/takeout/remote"
	"github.com/knoppiks/opencloud-backup-plugin/internal/targets"
	"github.com/knoppiks/opencloud-backup-plugin/pkg/keys"
)

// authOptions wires who a caller is: the token validator, the OpenCloud user
// id behind it, whether they are an admin, and their groups.
func authOptions(cfg api.Env, logger *slog.Logger) ([]api.Option, error) {
	var opts []api.Option
	// --- OIDC validator ---------------------------------------------------
	// An issuer without an audience or without OC_BASE_URL was refused by
	// api.Env.Validate.
	if issuer := cfg.OIDC.Issuer; issuer != "" {
		// Discovery happens in the background: an identity provider that is not
		// up yet must delay authentication, not the whole service (a
		// crash-looping pod is harder to diagnose than a 503 that explains
		// itself).
		ks := api.NewLazyKeySet(issuer, httpClient(), time.Hour, logger)
		v, err := api.NewOIDCValidator(api.OIDCConfig{
			Issuer:   issuer,
			Audience: cfg.OIDC.Audience,
			KeySet:   ks,
		})
		if err != nil {
			return nil, err
		}
		opts = append(opts, api.WithTokenValidator(v))

		// The token's `sub` is not the OpenCloud user id, and every
		// authorization decision is taken on the latter (internal/api/users.go).
		opts = append(opts, api.WithUserResolver(api.NewGraphUserResolver(cfg.OpenCloud.BaseURL, httpClient())))
	} else {
		logger.Warn("OIDC_ISSUER unset; authenticated routes will reject all requests")
	}

	// --- admin resolver ---------------------------------------------------
	// ADMIN_SUBJECT_ALLOWLIST holds OpenCloud user ids, despite its name,
	// which predates the finding in internal/api/users.go.
	if ids := cfg.OpenCloud.AdminAllowlist; len(ids) > 0 {
		opts = append(opts, api.WithAdminResolver(api.NewAllowlistAdminResolver(ids)))
		logger.Info("admin detection: allow-list of OpenCloud user ids", "count", len(ids))
	} else if base := cfg.OpenCloud.BaseURL; base != "" {
		roleID := cfg.OpenCloud.AdminAppRoleID
		opts = append(opts, api.WithAdminResolver(api.NewGraphAdminResolver(base, roleID, httpClient())))
		logger.Info("admin detection: graph appRoleAssignments", "admin_app_role_id", roleID)
	} else {
		logger.Warn("no admin resolver configured; admin routes will 403 for everyone")
	}

	// --- group resolver ---------------------------------------------------
	// Group grants on a Space are only honoured when the caller's groups can be
	// resolved. Without this, a Space granted to a group refuses the members who
	// need that grant rather than guessing (fail closed).
	if base := cfg.OpenCloud.BaseURL; base != "" {
		opts = append(opts, api.WithGroupResolver(api.NewGraphGroupResolver(base, httpClient())))
		logger.Info("group resolution: graph memberOf")
	} else {
		logger.Warn("OC_BASE_URL unset; group grants on a Space cannot be honoured")
	}

	return opts, nil
}

// custody is what the wrapping keys give the service: the envelope store,
// the SRW wrapper when SRW_KEY is set, the target credential sealer when
// TW_KEY is set.
type custody struct {
	keyStore *keystore.Store
	srw      *keys.SRWWrapper
	sealer   targets.CredSealer
}

// options hands the API what it may use. The API seals credentials and never
// opens them: a credential blob is opened in worker memory at run time and
// nowhere else (decisions.md #14).
func (c custody) options() []api.Option {
	// The store holds ciphertext only and is wired regardless of the SRW key:
	// the status board reads a Space's key state from it, and "is this Space
	// set up" must not become unanswerable because the key that *adds* a wrap
	// is missing. Setup itself still refuses without the SRW wrapper.
	opts := []api.Option{api.WithKeyStore(c.keyStore)}
	if c.srw != nil {
		opts = append(opts, api.WithSRWWrapper(c.srw))
	}
	if c.sealer != nil {
		opts = append(opts, api.WithCredSealer(c.sealer))
	}
	return opts
}

// buildCustody loads the wrapping keys and builds what uses them. The raw
// keys are wiped when it returns; the wrapper and sealer keep their own copy.
func buildCustody(cfg custodyEnv, backing state.Store, logger *slog.Logger) (custody, error) {
	// --- key service (Phase 3) -------------------------------------------
	// The SRW key is cluster/KMS custody (decisions.md #1): it arrives via a
	// Secret-backed env var, is used to wrap/unwrap DKs, and is NEVER logged.
	// Without it the backup key endpoints stay unavailable rather than running
	// in a degraded, insecure mode. The store holds wrapped envelopes only.
	wrapKeys, err := loadWrapKeys(cfg)
	if err != nil {
		return custody{}, err
	}
	defer wrapKeys.zeroize()

	c := custody{keyStore: keystore.New(backing, nil)}
	if wrapKeys.srw != nil {
		c.srw, err = keys.NewSRWWrapper(wrapKeys.srw)
		if err != nil {
			return custody{}, err
		}
		logger.Info("key service enabled")
	} else {
		logger.Warn("SRW_KEY unset; backup key endpoints will be unavailable")
	}

	// --- target credential sealer (decisions.md #14) ---------------------
	// The TW key is a distinct cluster/KMS secret so data-key custody and
	// target-credential custody rotate independently.
	if wrapKeys.tw != nil {
		c.sealer, err = targets.NewCredSealer(wrapKeys.tw)
		if err != nil {
			return custody{}, err
		}
		logger.Info("target credential sealing enabled")
	} else {
		logger.Warn("TW_KEY unset; backup runs will be unavailable")
	}

	return c, nil
}

// pipelineDeps is what the backup, restore and scheduling pipelines are built
// from; buildService has made every one of them already.
type pipelineDeps struct {
	cfg          serviceEnv
	workDir      string
	background   *jobs.Background
	spaceReader  cs3.SpaceReader
	spaceWriter  cs3.SpaceWriter
	spaceConfigs *spacecfg.StateStore
	targetStore  *targets.StateStore
	credSealer   targets.CredSealer
	keyStore     *keystore.Store
	srwWrapper   *keys.SRWWrapper
	jobStore     *jobs.StateStore
	locker       *jobs.LeaseLocker
	events       *notify.StateStore
	reporter     *notify.Reporter
	monitor      *notify.Monitor
	logger       *slog.Logger
}

// buildPipelines wires the backup runner, the restore runner and the
// scheduler that drives them.
func buildPipelines(d pipelineDeps) ([]api.Option, *scheduler.Scheduler, error) {
	parallelism := d.cfg.Backup.Parallelism
	engine, err := snapshot.NewEngine(s3repo.Opener{Limits: d.cfg.Backup.limits()}, snapshot.EngineOptions{
		Parallelism: parallelism,
		WorkDir:     d.workDir,
	})
	if err != nil {
		return nil, nil, err
	}

	runner, err := backup.NewRunner(backup.Deps{
		Background: d.background,
		Spaces:     d.spaceReader,
		Configs:    d.spaceConfigs,
		Targets:    d.targetStore,
		Sealer:     d.credSealer,
		Keys:       d.keyStore,
		Unwrap:     d.srwWrapper,
		Engine:     engine,
		Jobs:       d.jobStore,
		Locks:      d.locker,
		// Publishing the RK-wrapped envelope to the target is what makes an
		// admin Take-Out self-contained, so Path A works with OpenCloud
		// down. It is ciphertext the server cannot open (Phase 5).
		Envelopes: remote.S3Publisher{},
		// Asked on prune runs only: what this target can actually enforce,
		// observed rather than assumed from the version it claims to be
		// (decisions.md #8).
		Immutability: objstore.S3Prober{},
		Logger:       d.logger,
	})
	if err != nil {
		return nil, nil, err
	}
	opts := []api.Option{api.WithBackupRunner(runner)}

	// Restore Path B: same collaborators, plus the CS3 write path. It is a
	// user-only capability; the API gates every route on membership.
	restorer, err := restore.NewRunner(restore.Deps{
		Background: d.background,
		Spaces:     d.spaceReader,
		Writer:     d.spaceWriter,
		Configs:    d.spaceConfigs,
		Targets:    d.targetStore,
		Sealer:     d.credSealer,
		Keys:       d.keyStore,
		Unwrap:     d.srwWrapper,
		Engine:     engine,
		Jobs:       d.jobStore,
		Locks:      d.locker,
		Logger:     d.logger,
	})
	if err != nil {
		return nil, nil, err
	}
	opts = append(opts, api.WithRestoreRunner(restorer))

	d.logger.Info("backup and restore pipelines enabled", "parallelism", parallelism)

	// --- scheduler (Phase 6) ---------------------------------------------
	sched, err := scheduler.New(scheduler.Deps{
		Configs: d.spaceConfigs,
		Jobs:    d.jobStore,
		Runner: scheduler.RunnerFunc(func(ctx context.Context, spaceID string) error {
			_, err := runner.RunScheduled(ctx, spaceID)
			return err
		}),
		// Retention is applied by the same runner, as its own job kind:
		// nothing else ever expires a snapshot or reclaims the storage a
		// failed run left behind (decisions.md #9 Tier 1).
		Prunes: scheduler.PruneRunnerFunc(func(ctx context.Context, spaceID string) error {
			_, err := runner.RunPrune(ctx, spaceID)
			return err
		}),
		Recoverer: d.locker,
		// The run lock answers "is this Space already running" in one read,
		// for every process, instead of scanning run history every tick.
		Runs: d.locker,
		// Notifications are trimmed with the run history they describe;
		// nothing else in the service would ever trim them.
		Events:        d.events,
		Clock:         scheduler.SystemClock(),
		Logger:        d.logger,
		OnRunFinished: d.reporter.RunFinished,
		OnSweep:       d.monitor.Sweep,
	}, d.cfg.Scheduler.Options())
	if err != nil {
		return nil, nil, err
	}
	opts = append(opts, api.WithScheduleAdvisor(sched))
	return opts, sched, nil
}
