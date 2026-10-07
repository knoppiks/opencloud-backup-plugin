// Command backupd is the main backup service: HTTP API + worker + scheduler.
// It stays thin — all logic lives in /pkg/* behind interfaces (AGENTS.md layout
// rule). Phase 2 wired the authenticated user API over the CS3 gateway; Phase 4
// added the backup pipeline (CS3 -> kopia -> S3 target); Phase 6 makes it run
// unattended: durable state, a scheduler, and notifications.
//
// With an argument it is the operator CLI instead (rotate.go: rotate-srw,
// rotate-tw). The maintenance commands ship in the same binary because they need
// the same configuration, the same state Space and the same custody keys.
//
// Secrets arrive only through Secret-backed environment variables and are never
// logged: SRW_KEY / SRW_KEY_OLD (Data-Key custody), TW_KEY / TW_KEY_OLD
// (target-credential custody), SMTP_PASSWORD, BOOTSTRAP_S3_SECRET_ACCESS_KEY
// (optional first-start seeding), and the OpenCloud service-account
// credentials — of which the secret is the most valuable of the lot, because it
// reaches plaintext (decisions.md, threat model).
package main

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	// The timezone database is embedded so schedules can be expressed in the
	// family's local time from a scratch container (SCHEDULE_TIMEZONE).
	_ "time/tzdata"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	"github.com/kopia/kopia/repo/blob/throttling"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"opencloud-backup-plugin/pkg/api"
	"opencloud-backup-plugin/pkg/backup"
	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/cs3state"
	"opencloud-backup-plugin/pkg/instance"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/notify"
	"opencloud-backup-plugin/pkg/objstore"
	"opencloud-backup-plugin/pkg/ocversion"
	"opencloud-backup-plugin/pkg/restore"
	"opencloud-backup-plugin/pkg/scheduler"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/state"
	"opencloud-backup-plugin/pkg/takeout"
	"opencloud-backup-plugin/pkg/targets"
)

// Shutdown budget.
//
// A backup can legitimately take hours, so waiting for one to finish is not an
// option: shutdown cancels every run, and a run cut short fails through the
// normal path. What the drain must cover is that failure being *recorded*. A
// run whose outcome write is cut off stays "running" until its lease expires
// and the next start recovers it. So the drain is derived from the outcome
// write's own worst case, plus a margin for the run to notice the cancellation
// and give its lock back (review-2026-10.md F7).
//
// The whole budget, HTTP shutdown included, must fit in the deployment's
// terminationGracePeriodSeconds (deploy/deployment-backupd.yaml; a test holds
// the two together).
const (
	httpShutdownTimeout = 10 * time.Second
	drainMargin         = 5 * time.Second
	drainTimeout        = jobs.OutcomeWorstCase + drainMargin
)

// startupDeps are the parts of startup a test substitutes. Production wires
// productionStartup.
type startupDeps struct {
	// openState returns the durable state store.
	openState func(client *cs3.Client, logger *slog.Logger) (state.Store, error)
}

// productionStartup is what the service runs with.
func productionStartup() startupDeps {
	return startupDeps{openState: buildStateStore}
}

// service is everything main runs: the HTTP API and, when configured, the
// scheduler that makes backups unattended.
type service struct {
	api       *api.Server
	scheduler *scheduler.Scheduler
	// background tracks manual runs, which outlive the request that started
	// them but not the process.
	background *jobs.Background
	// openCloud watches the OpenCloud version; nil without OC_BASE_URL.
	openCloud *ocversion.Monitor
}

// How often the OpenCloud version is re-read once known, and how soon it is
// retried while OpenCloud has not answered yet (it may start after us). An
// upgrade is rare and changes nothing here but a log line and a notice, so the
// slow cadence is enough.
const (
	openCloudVersionInterval = time.Hour
	openCloudVersionRetry    = 30 * time.Second
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// newLogger is the service's logger. It writes to stderr: stdout belongs to
// what a command prints, and `STATE_SPACE_ID=$(backupd provision-state-space)`
// must capture the id and nothing else (review-2026-10.md F6).
func newLogger(stderr io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(stderr, nil))
}

// run is the whole program behind main, returning its exit code. main is the
// only place that exits, so every deferred cleanup on the way here has run —
// in particular the one that withdraws this instance's registration.
func run(args []string, stdout, stderr io.Writer) int {
	logger := newLogger(stderr)

	// With no arguments the binary is the service. With one it is an operator
	// tool: the maintenance commands ship in the same image because they need
	// the same configuration, the same state Space and the same custody keys.
	if len(args) > 0 {
		if err := runCommand(context.Background(), args[0], args[1:], logger, stdout); err != nil {
			logger.Error("command failed", "command", args[0], "err", err)
			return 1
		}
		return 0
	}
	if err := serve(logger, productionStartup()); err != nil {
		logger.Error("service stopped with an error", "err", err)
		return 1
	}
	return 0
}

// serve runs the API and, when configured, the scheduler until a signal
// arrives. Every return path, including a failed startup, has released what
// buildService acquired.
func serve(logger *slog.Logger, deps startupDeps) error {
	svc, cleanup, err := buildService(context.Background(), logger, deps)
	if err != nil {
		return fmt.Errorf("startup failed: %w", err)
	}
	defer cleanup()

	certFile, keyFile, err := tlsFiles()
	if err != nil {
		return fmt.Errorf("startup failed: %w", err)
	}

	basePath, err := resolveBasePath()
	if err != nil {
		return fmt.Errorf("startup failed: %w", err)
	}
	if basePath != "" {
		logger.Info("API mounted under a path prefix", "base_path", basePath)
	}

	addr := envOr("BACKUPD_ADDR", ":8080")
	httpSrv := &http.Server{
		Addr:    addr,
		Handler: mountBasePath(svc.api.Handler(), basePath),
		// net/http's own errors (TLS handshakes, malformed requests) go to
		// the structured log rather than to a plain line on stderr.
		ErrorLog: httpErrorLog(logger),
		// A stalled or slow client must not be able to hold a connection open
		// indefinitely. WriteTimeout is generous because a snapshot listing for
		// a large Space is served synchronously; backup runs are background jobs
		// and do not hold a request open.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var workers sync.WaitGroup
	if svc.openCloud != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			svc.openCloud.Run(ctx, openCloudVersionInterval, openCloudVersionRetry)
		}()
	}
	if svc.scheduler != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := svc.scheduler.Run(ctx); err != nil {
				logger.Error("scheduler stopped", "err", err)
			}
		}()
	}

	serveErr := make(chan error, 1)
	go func() {
		var err error
		if certFile != "" {
			logger.Info("backupd listening (TLS)", "addr", addr)
			err = httpSrv.ListenAndServeTLS(certFile, keyFile)
		} else {
			// Plain HTTP: a Data Key crosses this listener once per Space, at
			// key setup, so something in front of it must terminate TLS.
			logger.Info("backupd listening (plain HTTP; a TLS-terminating ingress is required)", "addr", addr)
			err = httpSrv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("http server failed: %w", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	return shutdown(httpSrv, svc.background, &workers, serveErr, logger)
}

// shutdown stops accepting work, cancels what is running, and waits for it to
// record how it ended.
//
// Manual runs are cancelled together with the scheduler's (the signal context
// already reached those), so every run in the process starts failing at the
// same moment and all of them share one drain budget.
func shutdown(
	httpSrv *http.Server,
	background *jobs.Background,
	workers *sync.WaitGroup,
	serveErr <-chan error,
	logger *slog.Logger,
) error {
	if background != nil {
		background.Stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer cancel()
	shutdownErr := httpSrv.Shutdown(shutdownCtx)

	if background != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			background.Wait()
		}()
	}
	if !waitFor(workers, drainTimeout) {
		logger.Warn("runs did not stop in time; their locks expire on their own")
	}

	select {
	case err := <-serveErr:
		return err
	default:
	}
	if shutdownErr != nil {
		return fmt.Errorf("graceful shutdown failed: %w", shutdownErr)
	}
	return nil
}

// waitFor waits for wg, reporting whether it finished within d.
func waitFor(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// buildService wires the API and scheduler from environment configuration.
// Missing OIDC/CS3 config is tolerated so the health endpoint stays up
// (protected routes then fail closed), but a misconfigured value that we can
// detect is a hard error.
//
// On error, everything acquired so far has already been released and the
// returned cleanup does nothing. That matters most for the instance record: a
// startup that fails after claiming it and leaves it live makes every restart
// within its TTL fail with "another instance is running", hiding the real
// error behind a misleading one (review-2026-10.md F1).
func buildService(ctx context.Context, logger *slog.Logger, deps startupDeps) (
	svc service, cleanup func(), err error,
) {
	var opts []api.Option
	cleanup = func() {}
	defer func() {
		if err != nil {
			cleanup()
			cleanup = func() {}
		}
	}()

	// --- unedited manifest -------------------------------------------------
	// First, because it is the cheapest check there is and because every
	// diagnosis after it would be of a symptom.
	if err := checkPlaceholders(os.Environ()); err != nil {
		return service{}, cleanup, err
	}

	// --- work directory ---------------------------------------------------
	// Checked before anything else: it is pure configuration, and a service
	// that will write the family's data somewhere it should not is worth
	// refusing before it accepts a single request.
	workDir, err := resolveWorkDir(logger)
	if err != nil {
		return service{}, cleanup, err
	}

	// --- OIDC validator ---------------------------------------------------
	issuer := os.Getenv("OIDC_ISSUER")
	if issuer != "" {
		audience := strings.TrimSpace(os.Getenv("OIDC_AUDIENCE"))
		if audience == "" {
			return service{}, cleanup, errors.New(
				"OIDC_AUDIENCE is required when OIDC_ISSUER is set: without it any token " +
					"the same issuer minted for any application is accepted here")
		}
		// Discovery happens in the background: an identity provider that is not
		// up yet must delay authentication, not the whole service (a
		// crash-looping pod is harder to diagnose than a 503 that explains
		// itself).
		ks := api.NewLazyKeySet(issuer, httpClient(), time.Hour, logger)
		v, err := api.NewOIDCValidator(api.OIDCConfig{
			Issuer:   issuer,
			Audience: audience,
			KeySet:   ks,
		})
		if err != nil {
			return service{}, cleanup, err
		}
		opts = append(opts, api.WithTokenValidator(v))

		// The token's `sub` is not the OpenCloud user id, and every
		// authorization decision is taken on the latter (pkg/api/users.go).
		// Without OC_BASE_URL nobody could be identified, so the service would
		// answer 503 to every user: refuse to start instead.
		base := strings.TrimSpace(os.Getenv("OC_BASE_URL"))
		if base == "" {
			return service{}, cleanup, errors.New(
				"OC_BASE_URL is required when OIDC_ISSUER is set: a caller's OpenCloud " +
					"user id is read from its graph API, and the token's subject is not that id")
		}
		opts = append(opts, api.WithUserResolver(api.NewGraphUserResolver(base, httpClient())))
	} else {
		logger.Warn("OIDC_ISSUER unset; authenticated routes will reject all requests")
	}

	// --- admin resolver ---------------------------------------------------
	// ADMIN_SUBJECT_ALLOWLIST holds OpenCloud user ids, despite its name,
	// which predates the finding in pkg/api/users.go.
	if allow := os.Getenv("ADMIN_SUBJECT_ALLOWLIST"); allow != "" {
		ids := splitAndTrim(allow)
		opts = append(opts, api.WithAdminResolver(api.NewAllowlistAdminResolver(ids)))
		logger.Info("admin detection: allow-list of OpenCloud user ids", "count", len(ids))
	} else if base := os.Getenv("OC_BASE_URL"); base != "" {
		roleID := envOr("OC_ADMIN_APP_ROLE_ID", api.DefaultAdminAppRoleID)
		opts = append(opts, api.WithAdminResolver(api.NewGraphAdminResolver(base, roleID, httpClient())))
		logger.Info("admin detection: graph appRoleAssignments", "admin_app_role_id", roleID)
	} else {
		logger.Warn("no admin resolver configured; admin routes will 403 for everyone")
	}

	// --- group resolver ---------------------------------------------------
	// Group grants on a Space are only honoured when the caller's groups can be
	// resolved. Without this, a Space granted to a group refuses the members who
	// need that grant rather than guessing (fail closed).
	if base := os.Getenv("OC_BASE_URL"); base != "" {
		opts = append(opts, api.WithGroupResolver(api.NewGraphGroupResolver(base, httpClient())))
		logger.Info("group resolution: graph memberOf")
	} else {
		logger.Warn("OC_BASE_URL unset; group grants on a Space cannot be honoured")
	}

	// --- OpenCloud version (compatibility-policy.md §3) ------------------
	// Read, logged, and warned about when it is outside the window this build
	// was tested in — never refused. Checked in the background by serve, so an
	// OpenCloud that is not up yet delays nothing.
	openCloud, err := buildOpenCloudMonitor(logger)
	if err != nil {
		return service{}, cleanup, err
	}
	if openCloud != nil {
		opts = append(opts, api.WithOpenCloudVersion(openCloud))
	}

	// --- CS3 space reader / writer ---------------------------------------
	var (
		spaceReader cs3.SpaceReader
		spaceWriter cs3.SpaceWriter
		cs3Client   *cs3.Client
	)
	client, closeCS3, err := dialCS3()
	if err != nil {
		return service{}, cleanup, err
	}
	if client != nil {
		cleanup = closeCS3
		spaceReader, spaceWriter, cs3Client = client, client, client
		opts = append(opts, api.WithSpaceReader(client))
	} else {
		logger.Warn("CS3_GATEWAY_ADDR unset; /api/v1/spaces will be unavailable")
	}

	// --- durable state (Phase 6) -----------------------------------------
	// Everything the service remembers — schedules, run history, wrapped key
	// envelopes, target records — lives in a dedicated OpenCloud Space. See
	// pkg/cs3state for the trade-offs; the short version is that a scheduler
	// whose memory dies with the process cannot tell a missed run from a fresh
	// install.
	backing, err := deps.openState(cs3Client, logger)
	if err != nil {
		return service{}, cleanup, err
	}

	// --- single-instance guard -------------------------------------------
	// Two instances against one state Space can mark each other's runs failed
	// and dispatch the same Space twice (decisions.md #16). This is a check, not
	// a lock — the backend has no compare-and-set — but it catches the case that
	// actually happens: a rolling update starting a second pod.
	guard, err := instance.New(backing, instance.Options{Logger: logger})
	if err != nil {
		return service{}, cleanup, err
	}
	standDown, err := guard.Claim(ctx)
	if err != nil {
		return service{}, cleanup, err
	}
	cleanup = chain(standDown, cleanup)
	logger.Info("instance registered", "instance", guard.ID())

	// --- target store / authorizer ---------------------------------------
	// One value, two roles: the authorizer answers what a user may see, the
	// store backs the admin API that decides it (decisions.md #12).
	targetStore := targets.NewStateStore(backing)
	opts = append(opts,
		api.WithAuthorizer(targetStore),
		api.WithTargetStore(targetStore),
		// The admin's connection check. Read-only and coarse towards the API;
		// the cause of a failure goes to this log only (pkg/objstore).
		api.WithTargetChecker(objstore.S3Checker{Logger: logger}),
	)

	// --- key service (Phase 3) -------------------------------------------
	// The SRW key is cluster/KMS custody (decisions.md #1): it arrives via a
	// Secret-backed env var, is used to wrap/unwrap DKs, and is NEVER logged.
	// Without it the backup key endpoints stay unavailable rather than running
	// in a degraded, insecure mode. The store holds wrapped envelopes only.
	wrapKeys, err := loadWrapKeys()
	if err != nil {
		return service{}, cleanup, err
	}
	defer wrapKeys.zeroize()

	var srwWrapper *keys.SRWWrapper
	keyStore := keys.NewStateStore(backing, nil)
	// The store holds ciphertext only and is wired regardless of the SRW key:
	// the status board reads a Space's key state from it, and "is this Space
	// set up" must not become unanswerable because the key that *adds* a wrap
	// is missing. Setup itself still refuses without the SRW wrapper.
	opts = append(opts, api.WithKeyStore(keyStore))
	if wrapKeys.srw != nil {
		srwWrapper, err = keys.NewSRWWrapper(wrapKeys.srw)
		if err != nil {
			return service{}, cleanup, err
		}
		opts = append(opts, api.WithSRWWrapper(srwWrapper))
		logger.Info("key service enabled")
	} else {
		logger.Warn("SRW_KEY unset; backup key endpoints will be unavailable")
	}

	// --- target credential sealer (decisions.md #14) ---------------------
	// The TW key is a distinct cluster/KMS secret so data-key custody and
	// target-credential custody rotate independently.
	var credSealer targets.CredSealer
	if wrapKeys.tw != nil {
		credSealer, err = targets.NewCredSealer(wrapKeys.tw)
		if err != nil {
			return service{}, cleanup, err
		}
		// The API seals with it and never opens: a credential blob is opened
		// in worker memory at run time and nowhere else (decisions.md #14).
		opts = append(opts, api.WithCredSealer(credSealer))
		logger.Info("target credential sealing enabled")
	} else {
		logger.Warn("TW_KEY unset; backup runs will be unavailable")
	}

	// --- optional default target ------------------------------------------
	// Seeding is skipped unless explicitly enabled and the store is empty, so it
	// can never override what an admin configured (decisions.md #12).
	if credSealer != nil {
		cfg := bootstrapConfig()
		seeded, err := targets.Bootstrap(ctx, targetStore, credSealer, cfg)
		if err != nil {
			return service{}, cleanup, err
		}
		if seeded {
			// Name and bucket are non-secret; credentials are never logged.
			logger.Info("seeded default backup target", "name", cfg.Name, "bucket", cfg.Bucket)
		}
	}

	// --- backup pipeline (Phase 4) ---------------------------------------
	spaceConfigs := spacecfg.NewStateStore(backing, nil)
	jobStore := jobs.NewStateStore(backing, nil)
	opts = append(opts, api.WithSpaceConfigStore(spaceConfigs), api.WithJobStore(jobStore))

	// The run lock is a lease so a crashed process cannot hold a Space forever.
	locker, err := jobs.NewLeaseLocker(backing, jobs.LeaseOptions{Logger: logger})
	if err != nil {
		return service{}, cleanup, err
	}
	// Anything left running by a previous incarnation is closed out before this
	// one starts scheduling, so history never shows an eternal "running".
	if recovered, err := locker.Recover(ctx, jobStore); err != nil {
		logger.Warn("could not recover abandoned runs at startup", "err", err)
	} else if recovered > 0 {
		logger.Warn("closed out runs abandoned by a previous process", "jobs", recovered)
	}

	// --- notifications (Phase 6) -----------------------------------------
	events := notify.NewStateStore(backing, nil)
	notifier, err := buildNotifier(events, logger)
	if err != nil {
		return service{}, cleanup, err
	}
	monitor, err := notify.NewMonitor(notify.MonitorDeps{
		Configs:  spaceConfigs,
		Jobs:     jobStore,
		Events:   events,
		Notifier: notifier,
		Logger:   logger,
	}, notify.MonitorOptions{})
	if err != nil {
		return service{}, cleanup, err
	}
	reporter := notify.NewReporter(notifier, classifyRunFailure, logger)
	opts = append(opts, api.WithNotificationStore(events))

	var sched *scheduler.Scheduler
	// Manual runs outlive their request but not the process: serve stops
	// this at shutdown, which cancels them, and waits for them to record how
	// they ended.
	background := jobs.NewBackground(logger)

	if spaceReader != nil && srwWrapper != nil && credSealer != nil {
		limits, err := bandwidthLimits()
		if err != nil {
			return service{}, cleanup, err
		}
		parallelism, err := envInt("BACKUP_PARALLELISM", 0)
		if err != nil {
			return service{}, cleanup, err
		}

		engine, err := snapshot.NewEngine(snapshot.S3Opener{Limits: limits}, snapshot.EngineOptions{
			Parallelism: parallelism,
			WorkDir:     workDir,
		})
		if err != nil {
			return service{}, cleanup, err
		}

		runner, err := backup.NewRunner(backup.Deps{
			Background: background,
			Spaces:     spaceReader,
			Configs:    spaceConfigs,
			Targets:    targetStore,
			Sealer:     credSealer,
			Keys:       keyStore,
			Unwrap:     srwWrapper,
			Engine:     engine,
			Jobs:       jobStore,
			Locks:      locker,
			// Publishing the RK-wrapped envelope to the target is what makes an
			// admin Take-Out self-contained, so Path A works with OpenCloud
			// down. It is ciphertext the server cannot open (Phase 5).
			Envelopes: takeout.S3Publisher{},
			// Asked on prune runs only: what this target can actually enforce,
			// observed rather than assumed from the version it claims to be
			// (decisions.md #8).
			Immutability: objstore.S3Prober{},
			Logger:       logger,
		})
		if err != nil {
			return service{}, cleanup, err
		}
		opts = append(opts, api.WithBackupRunner(runner))

		// Restore Path B: same collaborators, plus the CS3 write path. It is a
		// user-only capability; the API gates every route on membership.
		restorer, err := restore.NewRunner(restore.Deps{
			Background: background,
			Spaces:     spaceReader,
			Writer:     spaceWriter,
			Configs:    spaceConfigs,
			Targets:    targetStore,
			Sealer:     credSealer,
			Keys:       keyStore,
			Unwrap:     srwWrapper,
			Engine:     engine,
			Jobs:       jobStore,
			Locks:      locker,
			Logger:     logger,
		})
		if err != nil {
			return service{}, cleanup, err
		}
		opts = append(opts, api.WithRestoreRunner(restorer))

		logger.Info("backup and restore pipelines enabled", "parallelism", parallelism)

		// --- scheduler (Phase 6) ------------------------------------------
		schedOpts, err := schedulerOptions()
		if err != nil {
			return service{}, cleanup, err
		}
		sched, err = scheduler.New(scheduler.Deps{
			Configs: spaceConfigs,
			Jobs:    jobStore,
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
			Recoverer: locker,
			// The run lock answers "is this Space already running" in one read,
			// for every process, instead of scanning run history every tick.
			Runs: locker,
			// Notifications are trimmed with the run history they describe;
			// nothing else in the service would ever trim them.
			Events:        events,
			Clock:         scheduler.SystemClock(),
			Logger:        logger,
			OnRunFinished: reporter.RunFinished,
			OnSweep:       monitor.Sweep,
		}, schedOpts)
		if err != nil {
			return service{}, cleanup, err
		}
		opts = append(opts, api.WithScheduleAdvisor(sched))
	} else {
		logger.Warn("backup pipeline disabled; CS3_GATEWAY_ADDR, SRW_KEY and TW_KEY are all required")
	}

	// --- readiness --------------------------------------------------------
	opts = append(opts, api.WithReadiness(readiness(spaceReader)))

	// Request lines and the cause of every 5xx answer (pkg/api/observe.go).
	opts = append(opts, api.WithLogger(logger))

	return service{
		api:        api.NewServer(opts...),
		scheduler:  sched,
		background: background,
		openCloud:  openCloud,
	}, cleanup, nil
}

// buildOpenCloudMonitor watches OpenCloud's version through OC_BASE_URL, the
// same origin the Graph calls use. Without OC_BASE_URL there is nothing to ask,
// and the version stays unknown.
func buildOpenCloudMonitor(logger *slog.Logger) (*ocversion.Monitor, error) {
	pins, err := ocversion.Embedded()
	if err != nil {
		return nil, err
	}
	logger.Info("supported OpenCloud versions", "supported", pins.Window().String())

	base := strings.TrimSpace(os.Getenv("OC_BASE_URL"))
	if base == "" {
		logger.Warn("OC_BASE_URL unset; the OpenCloud version cannot be checked")
		return nil, nil
	}
	source := ocversion.StatusSource{BaseURL: base, Client: httpClient()}
	return ocversion.NewMonitor(source, pins.Window(), logger), nil
}

// dialCS3 connects to the CS3 gateway as the service account. It returns a nil
// client when CS3_GATEWAY_ADDR is unset — the service degrades, an operator
// command refuses — and always returns a usable close function.
func dialCS3() (*cs3.Client, func(), error) {
	noop := func() {}

	addr := os.Getenv("CS3_GATEWAY_ADDR")
	if addr == "" {
		return nil, noop, nil
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, noop, err
	}
	gw := gateway.NewGatewayAPIClient(conn)
	// Cached: a token is good for minutes and every gateway call needs one, so
	// minting per call would double the traffic this service sends OpenCloud
	// for no benefit. The cache honours the token's own expiry and drops it the
	// moment reva rejects one.
	auth := cs3.NewCachedAuth(cs3.ServiceAccountAuth{
		Gateway:  gw,
		ClientID: os.Getenv("OC_SERVICE_ACCOUNT_ID"),
		Secret:   os.Getenv("OC_SERVICE_ACCOUNT_SECRET"),
	})
	opts, err := cs3ClientOptions()
	if err != nil {
		_ = conn.Close()
		return nil, noop, err
	}
	client := cs3.NewClient(gw, auth, opts...)
	return client, func() { _ = conn.Close() }, nil
}

// cs3ClientOptions is the data-path configuration every CS3 client here uses.
//
// CS3_DATA_SERVER_URL is needed from OpenCloud 7.5 on, where the gateway hands
// out the storage provider's own address (http://localhost:9158/data by
// default) instead of the public data gateway. It names where that data server
// is reachable from this service — scheme and host only, the path OpenCloud
// returns is kept. It only applies to URLs handed out without a transfer
// token, so it is harmless on OpenCloud up to 7.4, which hands out the public
// data gateway with one (pkg/cs3/dataorigin.go).
func cs3ClientOptions() ([]cs3.ClientOption, error) {
	opts := []cs3.ClientOption{cs3.WithHTTPClient(dataGatewayClient())}
	raw := strings.TrimSpace(os.Getenv("CS3_DATA_SERVER_URL"))
	if raw == "" {
		return opts, nil
	}
	origin, err := cs3.ParseDataServerOrigin(raw)
	if err != nil {
		return nil, fmt.Errorf("CS3_DATA_SERVER_URL: %w", err)
	}
	return append(opts, cs3.WithDataServerOrigin(origin)), nil
}

// buildStateStore chooses where the service keeps its own state.
//
// Durable state is required, not defaulted away. Losing it costs every Space its
// schedule, its history and the server-side copy of its wrapped Data Key, and
// the loss happens on an ordinary pod restart — long after the person who
// completed the key ceremony has stopped watching. A deployment that genuinely
// wants throwaway state says so with STATE_BACKEND=memory.
func buildStateStore(client *cs3.Client, logger *slog.Logger) (state.Store, error) {
	spaceID := os.Getenv("STATE_SPACE_ID")

	if memoryStateRequested() {
		logger.Warn(stateBackendVar + "=" + stateBackendMemory + ": service state is in memory only. " +
			"Schedules, run history and key envelopes will not survive a restart")
		return state.NewMemoryStore(), nil
	}
	if spaceID == "" {
		return nil, fmt.Errorf(
			"STATE_SPACE_ID is required: without it schedules, run history and every wrapped "+
				"Data Key die with the process. Provision a state Space (see the runbook in "+
				"README.md), or set %s=%s to accept losing them", stateBackendVar, stateBackendMemory)
	}
	if client == nil {
		return nil, errors.New(
			"CS3_GATEWAY_ADDR is required to reach the state space named by STATE_SPACE_ID")
	}

	store, err := cs3state.New(client, cs3state.Options{
		SpaceID: spaceID,
		Prefix:  envOr("STATE_PREFIX", cs3state.DefaultPrefix),
		// The state Space is created by the service account (see
		// `backupd provision-state-space`), which leaves that account a manager
		// grant OpenCloud will not let anyone remove. Check needs to know which
		// principal that is in order to tell the service's own access apart
		// from an end user's.
		ServiceAccountID: os.Getenv("OC_SERVICE_ACCOUNT_ID"),
	})
	if err != nil {
		return nil, err
	}

	// A state Space an end user can reach is a Space an end user can empty, and
	// what they would be emptying is the only server-side copy of every wrapped
	// Data Key. That is a misconfiguration to refuse at boot, not to discover
	// later. A Space that cannot be checked at all is a different matter:
	// OpenCloud may simply not be up yet, and resolution is lazy for exactly
	// that reason, so it is a warning.
	checkCtx, cancel := context.WithTimeout(context.Background(), stateCheckTimeout)
	defer cancel()
	if err := store.Check(checkCtx); err != nil {
		if errors.Is(err, cs3state.ErrUnsafeStateSpace) {
			return nil, err
		}
		logger.Warn("could not verify the state space at startup; it will be resolved on first use", "err", err)
	}

	logger.Info("service state persisted in OpenCloud", "space", spaceID)
	return store, nil
}

// stateCheckTimeout bounds the startup validation of the state Space.
const stateCheckTimeout = 30 * time.Second

// buildNotifier wires the delivery sinks. Logs are always a sink; SMTP is added
// when the operator configured a mail server. The password is read from a
// Secret-backed variable and never logged.
func buildNotifier(events notify.Store, logger *slog.Logger) (*notify.Notifier, error) {
	sinks := []notify.Sink{notify.LogSink{Logger: logger}}

	port, err := envInt("SMTP_PORT", 0)
	if err != nil {
		return nil, err
	}
	cfg := notify.SMTPConfig{
		Host:       os.Getenv("SMTP_HOST"),
		Port:       port,
		Username:   os.Getenv("SMTP_USERNAME"),
		Password:   os.Getenv("SMTP_PASSWORD"),
		From:       os.Getenv("SMTP_FROM"),
		OperatorTo: os.Getenv("NOTIFY_OPERATOR_EMAIL"),
	}
	if cfg.Valid() {
		sink, err := notify.NewSMTPSink(cfg)
		if err != nil {
			return nil, err
		}
		sinks = append(sinks, sink)
		logger.Info("operator notifications will be emailed", "host", cfg.Host, "port", cfg.Port)
	} else {
		logger.Info("no SMTP configuration; notifications are recorded and logged only")
	}

	return notify.New(events, notify.Options{Sinks: sinks, Logger: logger})
}

// schedulerOptions reads the scheduler's tuning from the environment.
func schedulerOptions() (scheduler.Options, error) {
	maxConcurrent, err := envInt("SCHEDULER_MAX_CONCURRENT", 0)
	if err != nil {
		return scheduler.Options{}, err
	}
	historyDays, err := envInt("JOB_HISTORY_DAYS", 0)
	if err != nil {
		return scheduler.Options{}, err
	}
	// How often retention is *applied*. How much is kept is the Space owner's
	// setting; this is the operator's, and the default (daily) suits a target
	// that is somebody's spare disk.
	pruneHours, err := envInt("PRUNE_INTERVAL_HOURS", 0)
	if err != nil {
		return scheduler.Options{}, err
	}

	opts := scheduler.Options{
		MaxConcurrent: maxConcurrent,
		HistoryWindow: time.Duration(historyDays) * 24 * time.Hour,
		PruneInterval: time.Duration(pruneHours) * time.Hour,
		// "Nightly at half past two" means the family's night. The container's
		// own zone (TZ) is the closest thing to that this process can know, and
		// a deployment that sets TZ for its logs has already said which zone it
		// thinks in; defaulting to UTC would quietly disagree with it.
		Location: time.Local,
	}
	if name := os.Getenv("SCHEDULE_TIMEZONE"); name != "" {
		loc, err := time.LoadLocation(name)
		if err != nil {
			return scheduler.Options{}, fmt.Errorf("SCHEDULE_TIMEZONE %q is not a known timezone", name)
		}
		opts.Location = loc
	}
	return opts, nil
}

// classifyRunFailure decides who hears about a failed run. Only failures the
// operator can actually fix produce an operator event, and that event never
// names the Space (decisions.md #15) — which is why the classification lives
// here, where both the pipeline's errors and the notifier are in scope.
func classifyRunFailure(err error) notify.Classification {
	switch {
	case errors.Is(err, backup.ErrTargetUnavailable):
		return notify.Classification{
			MemberMessage:   "The backup target is unavailable, so this space was not backed up.",
			Operational:     true,
			OperatorMessage: "A backup target could not be used. Check its endpoint, bucket and credentials.",
		}
	case errors.Is(err, backup.ErrNotConfigured):
		return notify.Classification{
			MemberMessage: "Backup is not set up for this space, so nothing was backed up.",
		}
	case errors.Is(err, backup.ErrRunInProgress), errors.Is(err, context.Canceled):
		// Neither is a failure anybody needs to hear about: one means a run was
		// already happening, the other that the service was shutting down.
		return notify.Classification{Silent: true}
	default:
		return notify.DefaultClassification()
	}
}

// bootstrapConfig reads the optional default-target configuration. The
// non-secret fields come from a ConfigMap; the credentials must come from a
// Secret (decisions.md #14) and are handed straight to the sealer.
func bootstrapConfig() targets.BootstrapConfig {
	return targets.BootstrapConfig{
		Enable:       envBool("BOOTSTRAP_ENABLE"),
		ID:           os.Getenv("BOOTSTRAP_TARGET_ID"),
		Name:         os.Getenv("BOOTSTRAP_TARGET_NAME"),
		Endpoint:     os.Getenv("BOOTSTRAP_S3_ENDPOINT"),
		Region:       os.Getenv("BOOTSTRAP_S3_REGION"),
		Bucket:       os.Getenv("BOOTSTRAP_S3_BUCKET"),
		Prefix:       os.Getenv("BOOTSTRAP_S3_PREFIX"),
		UsePathStyle: envBool("BOOTSTRAP_S3_USE_PATH_STYLE"),
		DisableTLS:   envBool("BOOTSTRAP_S3_DISABLE_TLS"),
		Creds: targets.PlainCreds{
			AccessKeyID:     os.Getenv("BOOTSTRAP_S3_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("BOOTSTRAP_S3_SECRET_ACCESS_KEY"),
		},
		// Optional second credential, used only by prune runs (decisions.md #9,
		// Tier 2). Unset means the deployment has one key and both roles use
		// it; half-set is refused rather than silently falling back.
		MaintenanceCreds: targets.PlainCreds{
			AccessKeyID:     os.Getenv("BOOTSTRAP_S3_MAINTENANCE_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("BOOTSTRAP_S3_MAINTENANCE_SECRET_ACCESS_KEY"),
		},
	}
}

// envBool reports whether an environment variable is set to a truthy value.
func envBool(key string) bool {
	v, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(key)))
	return err == nil && v
}

// bandwidthLimits reads the optional upload/download caps applied to the S3
// target, in bytes per second. Zero means unlimited.
func bandwidthLimits() (throttling.Limits, error) {
	up, err := envInt("BACKUP_UPLOAD_BYTES_PER_SECOND", 0)
	if err != nil {
		return throttling.Limits{}, err
	}
	down, err := envInt("BACKUP_DOWNLOAD_BYTES_PER_SECOND", 0)
	if err != nil {
		return throttling.Limits{}, err
	}
	return throttling.Limits{
		UploadBytesPerSecond:   float64(up),
		DownloadBytesPerSecond: float64(down),
	}, nil
}

// envInt parses a non-negative integer environment variable.
func envInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	return v, nil
}

// dataGatewayClient streams file bytes from reva's data gateway. It has no
// overall timeout on purpose — a single large file may legitimately take a long
// while — but bounds the phases before the body starts flowing. Once bytes are
// moving the client's own inactivity guard takes over (cs3.DefaultStallTimeout),
// and the run itself is bounded by backup.DefaultRunTimeout.
func dataGatewayClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 60 * time.Second
	transport.TLSHandshakeTimeout = 15 * time.Second
	return &http.Client{Transport: transport}
}

// readiness reports ready when the CS3 space reader (if configured) responds.
func readiness(reader cs3.SpaceReader) func(context.Context) error {
	return func(ctx context.Context) error {
		if reader == nil {
			return nil
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, err := reader.ListSpaces(probeCtx)
		return err
	}
}

// wrapKeys holds the service's two custody keys while startup wires them. Both
// are nil when unset; the caller decides what that disables.
type wrapKeys struct {
	srw []byte
	tw  []byte
}

// String keeps the keys out of any accidental "%v" of this struct. Formatting
// key material is a mistake this type refuses to make possible.
func (k wrapKeys) String() string { return "wrapKeys{redacted}" }

// zeroize wipes both keys. The wrappers copy what they need, so nothing outside
// this struct depends on the buffers after wiring.
func (k wrapKeys) zeroize() {
	keys.Zeroize(k.srw)
	keys.Zeroize(k.tw)
}

// loadWrapKeys reads SRW_KEY and TW_KEY and refuses the one combination that
// looks like a working configuration but is not: the same key in both.
//
// SRW guards Data Keys, TW guards target credentials, and decisions.md keeps
// them distinct so the two can rotate independently — retiring a leaked target
// credential must not mean re-wrapping every Space's Data Key. Setting them to
// one value silently collapses that into a single blast radius, and the failure
// is invisible: everything works. It is a misconfiguration to catch at boot.
func loadWrapKeys() (wrapKeys, error) {
	srw, err := loadWrapKey("SRW_KEY")
	if err != nil {
		return wrapKeys{}, err
	}
	tw, err := loadWrapKey("TW_KEY")
	if err != nil {
		keys.Zeroize(srw)
		return wrapKeys{}, err
	}
	// Constant-time because both operands are secrets, and cheap either way.
	if srw != nil && tw != nil && subtle.ConstantTimeCompare(srw, tw) == 1 {
		keys.Zeroize(srw)
		keys.Zeroize(tw)
		return wrapKeys{}, errors.New(
			"SRW_KEY and TW_KEY must be different keys: data-key custody and " +
				"target-credential custody are separate on purpose (decisions.md #14)")
	}
	return wrapKeys{srw: srw, tw: tw}, nil
}

// loadWrapKey reads a base64-encoded 256-bit wrapping key (SRW or TW) from the
// environment. It returns (nil, nil) when unset so the caller can decide whether
// the feature is optional.
//
// The key value itself is never logged, and errors deliberately describe only
// the shape of the problem, never the value (AGENTS.md: never log key material).
func loadWrapKey(envVar string) ([]byte, error) {
	raw := os.Getenv(envVar)
	if raw == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%s must be base64-encoded", envVar)
	}
	if len(key) != keys.SRWKeySize {
		return nil, fmt.Errorf("%s must decode to %d bytes", envVar, keys.SRWKeySize)
	}
	return key, nil
}

// httpErrorLog bridges the *log.Logger net/http writes its own errors to into
// the service's structured log, at WARN: they are about a client or a
// connection, not about a request this service answered.
func httpErrorLog(logger *slog.Logger) *log.Logger {
	return slog.NewLogLogger(logger.Handler(), slog.LevelWarn)
}

func httpClient() *http.Client { return &http.Client{Timeout: 15 * time.Second} }

func splitAndTrim(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
