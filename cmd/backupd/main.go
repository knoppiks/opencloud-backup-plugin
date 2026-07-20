// Command backupd is the main backup service: HTTP API + worker + scheduler.
// It stays thin — all logic lives in /pkg/* behind interfaces (AGENTS.md layout
// rule). Phase 2 wires the authenticated user API (OIDC + admin middleware,
// space & granted-target endpoints) over the CS3 gateway.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"opencloud-backup-plugin/pkg/api"
	"opencloud-backup-plugin/pkg/cs3"
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
		client := cs3.NewClient(gw, auth)
		spaceReader = client
		opts = append(opts, api.WithSpaceReader(client))
	} else {
		logger.Warn("CS3_GATEWAY_ADDR unset; /api/v1/spaces will be unavailable")
	}

	// --- target authorizer -----------------------------------------------
	// Phase 2 uses the in-memory store as the reference authorizer; a persistent
	// store lands with the admin target-management phase. Bootstrap seeding
	// (non-secret metadata) is out of scope here.
	store := targets.NewMemoryStore()
	opts = append(opts, api.WithAuthorizer(store))

	// --- readiness --------------------------------------------------------
	opts = append(opts, api.WithReadiness(readiness(spaceReader)))

	return api.NewServer(opts...), cleanup, nil
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
