package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/knoppiks/opencloud-backup-plugin/internal/cli"
)

func TestProbeURL(t *testing.T) {
	cases := map[string]struct {
		l    listenerEnv
		want string
	}{
		"every address":      {l: listenerEnv{Addr: ":8080"}, want: "http://127.0.0.1:8080/readyz"},
		"every IPv4 address": {l: listenerEnv{Addr: "0.0.0.0:9000"}, want: "http://127.0.0.1:9000/readyz"},
		"every IPv6 address": {l: listenerEnv{Addr: "[::]:8080"}, want: "http://[::1]:8080/readyz"},
		"one address":        {l: listenerEnv{Addr: "10.0.0.5:8080"}, want: "http://10.0.0.5:8080/readyz"},
		"TLS": {
			l:    listenerEnv{Addr: ":8443", TLSCertFile: "/tls.crt", TLSKeyFile: "/tls.key"},
			want: "https://127.0.0.1:8443/readyz",
		},
		// The probes never carry the ingress's prefix.
		"base path ignored": {l: listenerEnv{Addr: ":8080", BasePath: "/backup"}, want: "http://127.0.0.1:8080/readyz"},
	}
	for name, c := range cases {
		got, err := probeURL(c.l, "/readyz")
		if err != nil || got != c.want {
			t.Errorf("%s: probeURL = %q, %v; want %q", name, got, err, c.want)
		}
	}
	for _, addr := range []string{"8080", "localhost:"} {
		if _, err := probeURL(listenerEnv{Addr: addr}, "/readyz"); err == nil {
			t.Errorf("probeURL(%q) succeeded", addr)
		}
	}
}

// probeTarget answers /readyz with readyCode and /healthz with 200, and
// records what was asked.
func probeTarget(readyCode int, asked *[]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*asked = append(*asked, r.URL.Path)
		switch r.URL.Path {
		case "/readyz":
			w.WriteHeader(readyCode)
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func listenAddr(srv *httptest.Server) string {
	return srv.Listener.Addr().String()
}

// An invalid service configuration does not fail the probe: it reads only
// the listener's variables.
var unrelatedBrokenEnviron = []string{"SRW_KEY=not-base64", "OIDC_ISSUER=not a url"}

func TestHealthcheck_Ready(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(probeTarget(http.StatusOK, &asked))
	defer srv.Close()

	env := append([]string{"BACKUPD_ADDR=" + listenAddr(srv)}, unrelatedBrokenEnviron...)
	if r := runBackupd(env, "healthcheck"); r.code != cli.ExitOK || r.stdout != "" {
		t.Fatalf("run = %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	if len(asked) != 1 || asked[0] != "/readyz" {
		t.Fatalf("asked %v", asked)
	}
}

func TestHealthcheck_NotReady(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(probeTarget(http.StatusServiceUnavailable, &asked))
	defer srv.Close()

	env := []string{"BACKUPD_ADDR=" + listenAddr(srv)}
	r := runBackupd(env, "healthcheck")
	if r.code != cli.ExitFailure || !strings.Contains(r.stderr, "status 503") {
		t.Fatalf("run = %d, stderr %q", r.code, r.stderr)
	}

	// -live asks whether the process answers, which it does.
	asked = nil
	if r := runBackupd(env, "healthcheck", "-live"); r.code != cli.ExitOK {
		t.Fatalf("-live: run = %d, stderr %q", r.code, r.stderr)
	}
	if len(asked) != 1 || asked[0] != "/healthz" {
		t.Fatalf("-live asked %v", asked)
	}
}

// A service terminating TLS itself is probed over HTTPS; its certificate
// names the public host, so it is not verified.
func TestHealthcheck_TLSListener(t *testing.T) {
	var asked []string
	srv := httptest.NewTLSServer(probeTarget(http.StatusOK, &asked))
	defer srv.Close()

	env := []string{
		"BACKUPD_ADDR=" + listenAddr(srv),
		"TLS_CERT_FILE=/etc/tls/tls.crt", "TLS_KEY_FILE=/etc/tls/tls.key",
	}
	if r := runBackupd(env, "healthcheck"); r.code != cli.ExitOK {
		t.Fatalf("run = %d, stderr %q", r.code, r.stderr)
	}
	if len(asked) != 1 {
		t.Fatalf("asked %v", asked)
	}
}

func TestHealthcheck_NothingListening(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := listenAddr(srv)
	srv.Close()

	r := runBackupd([]string{"BACKUPD_ADDR=" + addr}, "healthcheck", "-timeout", "2s")
	if r.code != cli.ExitFailure || !strings.Contains(r.stderr, "probe /readyz") {
		t.Fatalf("run = %d, stderr %q", r.code, r.stderr)
	}
}

// Its own variables are still checked: half a TLS configuration is refused.
func TestHealthcheck_InvalidListener(t *testing.T) {
	r := runBackupd([]string{"TLS_CERT_FILE=/tls.crt"}, "healthcheck")
	if r.code != cli.ExitFailure || !strings.Contains(r.stderr, "TLS_CERT_FILE and TLS_KEY_FILE") {
		t.Fatalf("run = %d, stderr %q", r.code, r.stderr)
	}
}

func TestHealthcheck_Usage(t *testing.T) {
	for _, args := range [][]string{
		{"healthcheck", "-timeout", "0s"},
		{"healthcheck", "-ready"},
		{"healthcheck", "extra"},
	} {
		if r := runBackupd(nil, args...); r.code != cli.ExitUsage {
			t.Errorf("%v: run = %d, stderr %q", args, r.code, r.stderr)
		}
	}
}
