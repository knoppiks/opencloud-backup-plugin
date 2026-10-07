package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/state"
)

func encodedKey(t *testing.T) string {
	t.Helper()
	key, err := keys.GenerateSRWKey()
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

// testConfig loads a configuration from the given entries alone, so a test
// states everything it depends on and nothing leaks in from the environment
// the tests run in.
func testConfig(t *testing.T, environ ...string) serviceEnv {
	t.Helper()
	cfg, err := loadServiceEnv(environ)
	if err != nil {
		t.Fatalf("loadServiceEnv: %v", err)
	}
	return cfg
}

// Shape and distinctness are custodyEnv's checks (env.go); what is left here
// is turning valid keys into the bytes the wrappers take.
func TestLoadWrapKeysDecodesBothKeys(t *testing.T) {
	srw, tw := encodedKey(t), encodedKey(t)
	loaded, err := loadWrapKeys(testConfig(t, "SRW_KEY="+srw, "TW_KEY="+tw).Keys)
	if err != nil {
		t.Fatalf("loadWrapKeys: %v", err)
	}
	defer loaded.zeroize()

	wantSRW, err := base64.StdEncoding.DecodeString(srw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded.srw, wantSRW) {
		t.Fatal("SRW_KEY was not loaded")
	}
	if len(loaded.tw) != keys.SRWKeySize {
		t.Fatalf("TW key length = %d", len(loaded.tw))
	}
	if strings.Contains(fmt.Sprintf("%v %+v", loaded, loaded), srw) {
		t.Fatal("formatting the loaded keys prints them")
	}
}

// Either key may be absent — that disables a feature, it is not an error.
func TestLoadWrapKeysToleratesUnsetKeys(t *testing.T) {
	for name, env := range map[string][]string{
		"neither set": nil,
		"only srw":    {"SRW_KEY=" + encodedKey(t)},
		"only tw":     {"TW_KEY=" + encodedKey(t)},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t, env...)
			loaded, err := loadWrapKeys(cfg.Keys)
			if err != nil {
				t.Fatalf("loadWrapKeys: %v", err)
			}
			defer loaded.zeroize()

			if cfg.Keys.SRW.IsSet() != (loaded.srw != nil) {
				t.Fatalf("SRW key presence = %v", loaded.srw != nil)
			}
			if cfg.Keys.TW.IsSet() != (loaded.tw != nil) {
				t.Fatalf("TW key presence = %v", loaded.tw != nil)
			}
		})
	}
}

// A misconfiguration stops the service and every command before anything
// else happens, and says what is wrong on stderr.
func TestRun_RefusesAnInvalidConfiguration(t *testing.T) {
	environ := []string{"OIDC_ISSUER=https://issuer.invalid", "OIDC_AUDIENCE=web"}
	for name, args := range map[string][]string{
		"service": nil,
		"command": {"provision-state-space"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(args, environ, &stdout, &stderr); code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if !strings.Contains(stderr.String(), "OC_BASE_URL is required") {
				t.Fatalf("stderr = %s", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want nothing", stdout.String())
			}
		})
	}
}

// The effective configuration is logged once at startup, before anything can
// fail, and no secret's value is in it.
func TestServe_LogsTheEffectiveConfigurationWithoutSecrets(t *testing.T) {
	srw, tw, password := encodedKey(t), encodedKey(t), encodedKey(t)
	cfg := testConfig(t,
		"SRW_KEY="+srw, "TW_KEY="+tw, "SMTP_PASSWORD="+password,
		"OC_BASE_URL=https://cloud.example",
		"BACKUP_WORK_DIR_ALLOW_DISK=true", "BACKUP_WORK_DIR="+t.TempDir(),
	)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	failing := startupDeps{openState: func(stateEnv, *cs3.Client, string, *slog.Logger) (state.Store, error) {
		return nil, errors.New("no state for this test")
	}}
	if err := serve(cfg, logger, failing); err == nil {
		t.Fatal("serve succeeded without state")
	}

	var line string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, `"msg":"configuration"`) {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no configuration line in %s", logs.String())
	}
	for _, secret := range []string{srw, tw, password} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("a secret reached the log")
		}
	}
	for _, want := range []string{`"oc_base_url":"https://cloud.example"`, `"srw_key":"[redacted]"`, `"tw_key_old":""`} {
		if !strings.Contains(line, want) {
			t.Errorf("configuration line lacks %s: %s", want, line)
		}
	}
}

func TestBuildOpenCloudMonitor(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)

	m, err := buildOpenCloudMonitor("", logger)
	if err != nil || m != nil {
		t.Fatalf("without OC_BASE_URL: monitor %v, err %v; want neither", m, err)
	}

	m, err = buildOpenCloudMonitor("https://cloud.example", logger)
	if err != nil || m == nil {
		t.Fatalf("with OC_BASE_URL: monitor %v, err %v", m, err)
	}
	// Nothing is fetched until serve runs it, so a down OpenCloud cannot
	// delay startup.
	if st := m.Status(); st.Known || st.Window.String() == "" {
		t.Errorf("fresh monitor status = %+v", st)
	}
}

// net/http's own errors reach the structured log, not a bare line on stderr.
func TestHTTPServerErrorsGoToTheStructuredLog(t *testing.T) {
	var (
		mu   sync.Mutex
		logs bytes.Buffer
	)
	logger := slog.New(slog.NewJSONHandler(lockedWriter{&mu, &logs}, nil))

	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.Config.ErrorLog = httpErrorLog(logger)
	srv.StartTLS()
	defer srv.Close()

	// Plain HTTP to a TLS listener: net/http logs a handshake error.
	resp, err := http.Get("http://" + srv.Listener.Addr().String())
	if err == nil {
		_ = resp.Body.Close()
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := logs.String()
		mu.Unlock()
		if strings.Contains(got, "TLS handshake error") {
			if !strings.Contains(got, `"level":"WARN"`) {
				t.Fatalf("http server error not logged at WARN: %s", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the TLS handshake error never reached the structured log")
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
