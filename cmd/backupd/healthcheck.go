package main

// Operator command: ask the running service whether it is ready.
//
// The image has no shell and no curl, so a container runtime without HTTP
// probes (Docker Compose's healthcheck) runs the binary itself. It asks the
// service in the same container over loopback and exits 0 or 1, which is what
// Docker reads as healthy or unhealthy.
//
// Readiness, not liveness, by default: Docker has one healthcheck where
// Kubernetes has two, and "healthy" should mean the service can do its job,
// which it cannot while OpenCloud is unreachable. -live asks /healthz instead.
//
// It reads only the listener's variables. A probe every few seconds has no
// business decoding the custody keys, and would otherwise report a
// configuration problem the running service already reported at startup.

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/knoppiks/opencloud-backup-plugin/internal/cli"
	"github.com/knoppiks/opencloud-backup-plugin/internal/config"
)

// defaultProbeTimeout bounds one probe. Readiness asks the CS3 gateway, so it
// is not instant; Docker's own healthcheck timeout (default 30s) is the outer
// bound.
const defaultProbeTimeout = 10 * time.Second

// loadListenerEnv reads only the HTTP listener's variables.
func loadListenerEnv(environ []string) (serviceEnv, error) {
	env := newServiceEnv()
	if err := config.Load(environ, &env.HTTP); err != nil {
		return serviceEnv{}, err
	}
	return serviceEnv{HTTP: env.HTTP}, nil
}

func parseHealthcheck(args []string, stdout, stderr io.Writer) (action, error) {
	fs := flag.NewFlagSet("backupd healthcheck", flag.ContinueOnError)
	live := fs.Bool("live", false, "ask /healthz (the process answers) instead of /readyz (it can reach OpenCloud)")
	timeout := fs.Duration("timeout", defaultProbeTimeout, "give up after this long")
	fs.Usage = func() {
		_, _ = fmt.Fprint(fs.Output(), `usage: backupd healthcheck [-live] [-timeout d]

Asks the service in this container whether it is ready, over loopback, at
the address BACKUPD_ADDR names (HTTPS when TLS_CERT_FILE is set). Exit
status 0 means ready, 1 not. For a container healthcheck, e.g. in Compose:

  healthcheck:
    test: ["CMD", "/backupd", "healthcheck"]

Reads only BACKUPD_ADDR and TLS_CERT_FILE / TLS_KEY_FILE.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := cli.Parse(fs, args, stdout, stderr); err != nil {
		return nil, err
	}
	if *timeout <= 0 {
		return nil, cli.Usagef("-timeout must be positive")
	}
	path := "/readyz"
	if *live {
		path = "/healthz"
	}
	return func(ctx context.Context, cfg serviceEnv, _ *slog.Logger, _ io.Writer) error {
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		return probe(ctx, cfg.HTTP, path)
	}, nil
}

// probe asks the service's own listener for path and fails unless it
// answers 200.
func probe(ctx context.Context, l listenerEnv, path string) error {
	target, err := probeURL(l, path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("probe %s: %w", path, err)
	}
	resp, err := probeClient().Do(req)
	if err != nil {
		return fmt.Errorf("probe %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe %s: status %d", path, resp.StatusCode)
	}
	return nil
}

// probeURL is where the listener answers from inside its own container: the
// loopback address for a listener on every address, otherwise the one it
// names. The health probes are never under BACKUPD_BASE_PATH.
func probeURL(l listenerEnv, path string) (string, error) {
	host, port, err := net.SplitHostPort(l.Addr)
	if err != nil {
		return "", fmt.Errorf("BACKUPD_ADDR %q: %w", l.Addr, err)
	}
	if port == "" {
		return "", errors.New("BACKUPD_ADDR names no port")
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	scheme := "http"
	if l.TLSCertFile != "" {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, port) + path, nil
}

// probeClient never uses a proxy, and does not verify the service's
// certificate: it names the public host, not loopback. The probe sends
// nothing and reads only a status, like Kubernetes' own HTTPS probes.
func probeClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: nil,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // loopback probe of our own listener; carries no data
				MinVersion:         tls.VersionTLS12,
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
