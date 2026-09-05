// Command backupd is the main backup service: HTTP API + worker + scheduler.
// It stays thin — all logic lives in /pkg/* behind interfaces (AGENTS.md layout
// rule). Phase 2 wired the authenticated user API over the CS3 gateway; Phase 4
// adds the backup pipeline (CS3 -> kopia -> S3 target) behind a manual trigger.
//
// Secrets arrive only through Secret-backed environment variables and are never
// logged: SRW_KEY (Data-Key custody), TW_KEY (target-credential custody), and
// the OpenCloud service-account credentials.
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
	"syscall"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	"github.com/kopia/kopia/repo/blob/throttling"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"opencloud-backup-plugin/pkg/api"
	"opencloud-backup-plugin/pkg/backup"
	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/targets"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	srv, cleanup, err := buildServer(context.Background(), logger)
	if err != nil {
		logger.Error("startup failed", "err", err)
		os.Exit(1)
	}
	defer cleanup()

	addr := envOr("BACKUPD_ADDR", ":8080")
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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
}

// buildServer wires the API from environment configuration. Missing OIDC/CS3
// config is tolerated so the health endpoint stays up (protected routes then
// fail closed), but a misconfigured value that we can detect is a hard error.
func buildServer(ctx context.Context, logger *slog.Logger) (*api.Server, func(), error) {
	var opts []api.Option
	cleanup := func() {}

	// --- OIDC validator ---------------------------------------------------
	issuer := os.Getenv("OIDC_ISSUER")
	if issuer != "" {
		ks, err := api.DiscoverKeySet(ctx, issuer, httpClient(), time.Hour)
		if err != nil {
			return nil, cleanup, err
		}
		v, err := api.NewOIDCValidator(api.OIDCConfig{
			Issuer:   issuer,
			Audience: os.Getenv("OIDC_AUDIENCE"),
			KeySet:   ks,
		})
		if err != nil {
			return nil, cleanup, err
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

	// --- CS3 space reader -------------------------------------------------
	var spaceReader cs3.SpaceReader
	if addr := os.Getenv("CS3_GATEWAY_ADDR"); addr != "" {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, cleanup, err
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
		opts = append(opts, api.WithSpaceReader(client))
	} else {
		logger.Warn("CS3_GATEWAY_ADDR unset; /api/v1/spaces will be unavailable")
	}

	// --- target store / authorizer ---------------------------------------
	// Phase 2 uses the in-memory store as the reference authorizer; a persistent
	// store lands with the admin target-management phase. Bootstrap seeding
	// (non-secret metadata) is out of scope here.
	targetStore := targets.NewMemoryStore()
	opts = append(opts, api.WithAuthorizer(targetStore))

	// --- key service (Phase 3) -------------------------------------------
	// The SRW key is cluster/KMS custody (decisions.md #1): it arrives via a
	// Secret-backed env var, is used to wrap/unwrap DKs, and is NEVER logged.
	// Without it the backup key endpoints stay unavailable rather than running
	// in a degraded, insecure mode.
	var srwWrapper *keys.SRWWrapper
	keyStore := keys.NewMemoryStore()
	if srwKey, err := loadWrapKey("SRW_KEY"); err != nil {
		return nil, cleanup, err
	} else if srwKey != nil {
		srwWrapper, err = keys.NewSRWWrapper(srwKey)
		keys.Zeroize(srwKey)
		if err != nil {
			return nil, cleanup, err
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
		return nil, cleanup, err
	} else if twKey != nil {
		credSealer, err = targets.NewCredSealer(twKey)
		keys.Zeroize(twKey)
		if err != nil {
			return nil, cleanup, err
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
			return nil, cleanup, err
		}
		if seeded {
			// Name and bucket are non-secret; credentials are never logged.
			logger.Info("seeded default backup target", "name", cfg.Name, "bucket", cfg.Bucket)
		}
	}

	// --- backup pipeline (Phase 4) ---------------------------------------
	spaceConfigs := spacecfg.NewMemoryStore()
	jobStore := jobs.NewMemoryStore()
	opts = append(opts, api.WithSpaceConfigStore(spaceConfigs), api.WithJobStore(jobStore))

	if spaceReader != nil && srwWrapper != nil && credSealer != nil {
		limits, err := bandwidthLimits()
		if err != nil {
			return nil, cleanup, err
		}
		parallelism, err := envInt("BACKUP_PARALLELISM", 0)
		if err != nil {
			return nil, cleanup, err
		}

		engine, err := snapshot.NewEngine(snapshot.S3Opener{Limits: limits}, snapshot.EngineOptions{
			Parallelism: parallelism,
			WorkDir:     os.Getenv("BACKUP_WORK_DIR"),
		})
		if err != nil {
			return nil, cleanup, err
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
			Locks:   jobStore,
			Logger:  logger,
		})
		if err != nil {
			return nil, cleanup, err
		}
		opts = append(opts, api.WithBackupRunner(runner))
		logger.Info("backup pipeline enabled", "parallelism", parallelism)
	} else {
		logger.Warn("backup pipeline disabled; CS3_GATEWAY_ADDR, SRW_KEY and TW_KEY are all required")
	}

	// --- readiness --------------------------------------------------------
	opts = append(opts, api.WithReadiness(readiness(spaceReader)))

	return api.NewServer(opts...), cleanup, nil
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
