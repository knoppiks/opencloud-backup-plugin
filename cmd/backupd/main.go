// Command backupd is the main backup service: HTTP API + worker + scheduler.
// It stays thin — all logic lives in /pkg/* behind interfaces (AGENTS.md layout
// rule). Phase 2 wired the authenticated user API over the CS3 gateway; Phase 4
// added the backup pipeline (CS3 -> kopia -> S3 target); Phase 6 makes it run
// unattended: durable state, a scheduler, and notifications.
//
// Secrets arrive only through Secret-backed environment variables and are never
// logged: SRW_KEY (Data-Key custody), TW_KEY (target-credential custody),
// SMTP_PASSWORD, and the OpenCloud service-account credentials.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
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
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/notify"
	"opencloud-backup-plugin/pkg/restore"
	"opencloud-backup-plugin/pkg/scheduler"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/state"
	"opencloud-backup-plugin/pkg/takeout"
	"opencloud-backup-plugin/pkg/targets"
)

// schedulerDrainTimeout bounds how long shutdown waits for in-flight scheduled
// runs. A backup can legitimately take hours, so waiting for one to finish is
// not an option; a run cut short here fails through the normal path, or — if
// the process dies first — is recovered from its lease on the next start.
const schedulerDrainTimeout = 30 * time.Second

// service is everything main runs: the HTTP API and, when configured, the
// scheduler that makes backups unattended.
type service struct {
	api       *api.Server
	scheduler *scheduler.Scheduler
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	svc, cleanup, err := buildService(context.Background(), logger)
	if err != nil {
		logger.Error("startup failed", "err", err)
		os.Exit(1)
	}
	defer cleanup()

	addr := envOr("BACKUPD_ADDR", ":8080")
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           svc.api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var workers sync.WaitGroup
	if svc.scheduler != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := svc.scheduler.Run(ctx); err != nil {
				logger.Error("scheduler stopped", "err", err)
			}
		}()
	}

	go func() {
		logger.Info("backupd listening", "addr", addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
	if !waitFor(&workers, schedulerDrainTimeout) {
		logger.Warn("scheduled runs did not stop in time; their locks expire on their own")
	}
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
func buildService(ctx context.Context, logger *slog.Logger) (service, func(), error) {
	var opts []api.Option
	cleanup := func() {}

	// --- OIDC validator ---------------------------------------------------
	issuer := os.Getenv("OIDC_ISSUER")
	if issuer != "" {
		ks, err := api.DiscoverKeySet(ctx, issuer, httpClient(), time.Hour)
		if err != nil {
			return service{}, cleanup, err
		}
		v, err := api.NewOIDCValidator(api.OIDCConfig{
			Issuer:   issuer,
			Audience: os.Getenv("OIDC_AUDIENCE"),
			KeySet:   ks,
		})
		if err != nil {
			return service{}, cleanup, err
		}
		opts = append(opts, api.WithTokenValidator(v))
	} else {
		logger.Warn("OIDC_ISSUER unset; authenticated routes will reject all requests")
	}

	// --- admin resolver ---------------------------------------------------
	if allow := os.Getenv("ADMIN_SUBJECT_ALLOWLIST"); allow != "" {
		subs := splitAndTrim(allow)
		opts = append(opts, api.WithAdminResolver(api.NewAllowlistAdminResolver(subs)))
		logger.Info("admin detection: allow-list", "count", len(subs))
	} else if base := os.Getenv("OC_BASE_URL"); base != "" {
		roleID := envOr("OC_ADMIN_APP_ROLE_ID", api.DefaultAdminAppRoleID)
		opts = append(opts, api.WithAdminResolver(api.NewGraphAdminResolver(base, roleID, httpClient())))
		logger.Info("admin detection: graph appRoleAssignments", "adminAppRoleId", roleID)
	} else {
		logger.Warn("no admin resolver configured; admin routes will 403 for everyone")
	}

	// --- CS3 space reader / writer ---------------------------------------
	var (
		spaceReader cs3.SpaceReader
		spaceWriter cs3.SpaceWriter
		cs3Client   *cs3.Client
	)
	if addr := os.Getenv("CS3_GATEWAY_ADDR"); addr != "" {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return service{}, cleanup, err
		}
		cleanup = func() { _ = conn.Close() }
		gw := gateway.NewGatewayAPIClient(conn)
		auth := cs3.ServiceAccountAuth{
			Gateway:  gw,
			ClientID: os.Getenv("OC_SERVICE_ACCOUNT_ID"),
			Secret:   os.Getenv("OC_SERVICE_ACCOUNT_SECRET"),
		}
		client := cs3.NewClient(gw, auth, cs3.WithHTTPClient(dataGatewayClient()))
		spaceReader = client
		spaceWriter = client
		cs3Client = client
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
	backing, err := buildStateStore(cs3Client, logger)
	if err != nil {
		return service{}, cleanup, err
	}

	// --- target store / authorizer ---------------------------------------
	targetStore := targets.NewStateStore(backing)
	opts = append(opts, api.WithAuthorizer(targetStore))

	// --- key service (Phase 3) -------------------------------------------
	// The SRW key is cluster/KMS custody (decisions.md #1): it arrives via a
	// Secret-backed env var, is used to wrap/unwrap DKs, and is NEVER logged.
	// Without it the backup key endpoints stay unavailable rather than running
	// in a degraded, insecure mode. The store holds wrapped envelopes only.
	var srwWrapper *keys.SRWWrapper
	keyStore := keys.NewStateStore(backing, nil)
	if srwKey, err := loadWrapKey("SRW_KEY"); err != nil {
		return service{}, cleanup, err
	} else if srwKey != nil {
		srwWrapper, err = keys.NewSRWWrapper(srwKey)
		keys.Zeroize(srwKey)
		if err != nil {
			return service{}, cleanup, err
		}
		opts = append(opts, api.WithKeyStore(keyStore), api.WithSRWWrapper(srwWrapper))
		logger.Info("key service enabled")
	} else {
		logger.Warn("SRW_KEY unset; backup key endpoints will be unavailable")
	}

	// --- target credential sealer (decisions.md #14) ---------------------
	// The TW key is a distinct cluster/KMS secret so data-key custody and
	// target-credential custody rotate independently.
	var credSealer targets.CredSealer
	if twKey, err := loadWrapKey("TW_KEY"); err != nil {
		return service{}, cleanup, err
	} else if twKey != nil {
		credSealer, err = targets.NewCredSealer(twKey)
		keys.Zeroize(twKey)
		if err != nil {
			return service{}, cleanup, err
		}
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
			WorkDir:     os.Getenv("BACKUP_WORK_DIR"),
		})
		if err != nil {
			return service{}, cleanup, err
		}

		runner, err := backup.NewRunner(backup.Deps{
			Spaces:  spaceReader,
			Configs: spaceConfigs,
			Targets: targetStore,
			Sealer:  credSealer,
			Keys:    keyStore,
			Unwrap:  srwWrapper,
			Engine:  engine,
			Jobs:    jobStore,
			Locks:   locker,
			// Publishing the RK-wrapped envelope to the target is what makes an
			// admin Take-Out self-contained, so Path A works with OpenCloud
			// down. It is ciphertext the server cannot open (Phase 5).
			Envelopes: takeout.S3Publisher{},
			Logger:    logger,
		})
		if err != nil {
			return service{}, cleanup, err
		}
		opts = append(opts, api.WithBackupRunner(runner))

		// Restore Path B: same collaborators, plus the CS3 write path. It is a
		// user-only capability; the API gates every route on membership.
		restorer, err := restore.NewRunner(restore.Deps{
			Spaces:  spaceReader,
			Writer:  spaceWriter,
			Configs: spaceConfigs,
			Targets: targetStore,
			Sealer:  credSealer,
			Keys:    keyStore,
			Unwrap:  srwWrapper,
			Engine:  engine,
			Jobs:    jobStore,
			Locks:   locker,
			Logger:  logger,
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
			Recoverer:     locker,
			Clock:         scheduler.SystemClock(),
			Logger:        logger,
			OnRunFinished: reporter.RunFinished,
			OnTick:        monitor.Sweep,
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

	return service{api: api.NewServer(opts...), scheduler: sched}, cleanup, nil
}

// buildStateStore chooses where the service keeps its own state. Without a
// state Space it degrades to memory and says so loudly: that mode is only
// sensible for a smoke test, because schedules, history and key envelopes then
// vanish on restart.
func buildStateStore(client *cs3.Client, logger *slog.Logger) (state.Store, error) {
	spaceID := os.Getenv("STATE_SPACE_ID")
	if spaceID == "" || client == nil {
		logger.Warn("STATE_SPACE_ID unset or CS3 unavailable; service state is in memory only. " +
			"Schedules, run history and key envelopes will not survive a restart")
		return state.NewMemoryStore(), nil
	}

	store, err := cs3state.New(client, cs3state.Options{
		SpaceID: spaceID,
		Prefix:  envOr("STATE_PREFIX", cs3state.DefaultPrefix),
	})
	if err != nil {
		return nil, err
	}
	logger.Info("service state persisted in OpenCloud", "space", spaceID)
	return store, nil
}

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

	opts := scheduler.Options{
		MaxConcurrent: maxConcurrent,
		HistoryWindow: time.Duration(historyDays) * 24 * time.Hour,
		Location:      time.UTC,
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
// while — but bounds the phases before the body starts flowing, so a wedged
// gateway cannot stall a run indefinitely. The run itself is bounded by
// backup.DefaultRunTimeout.
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
