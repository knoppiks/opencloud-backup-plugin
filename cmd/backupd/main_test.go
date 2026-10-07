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

	"github.com/knoppiks/opencloud-backup-plugin/internal/api"
	"github.com/knoppiks/opencloud-backup-plugin/internal/buildinfo"
	"github.com/knoppiks/opencloud-backup-plugin/internal/config"
	"github.com/knoppiks/opencloud-backup-plugin/internal/cs3"
	"github.com/knoppiks/opencloud-backup-plugin/internal/cs3state"
	"github.com/knoppiks/opencloud-backup-plugin/internal/jobs"
	"github.com/knoppiks/opencloud-backup-plugin/internal/scheduler"
	"github.com/knoppiks/opencloud-backup-plugin/internal/state"
	"github.com/knoppiks/opencloud-backup-plugin/pkg/keys"
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
func testConfig(t *testing.T, environ ...string) config.Backupd {
	t.Helper()
	cfg, err := config.LoadBackupd(environ)
	if err != nil {
		t.Fatalf("config.LoadBackupd: %v", err)
	}
	return cfg
}

// Shape and distinctness are config's checks (internal/config); what is left
// here is turning valid keys into the bytes the wrappers take.
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

// "Nightly at half past two" is about the family's night. The container's own
// zone is the closest thing this process can know, and a deployment that sets
// TZ has already said which zone it thinks in.
func TestSchedulerOptions_DefaultsToTheContainerTimezone(t *testing.T) {
	// Go reads TZ once, at first use; set what it would have read.
	time.Local = mustLoadLocation(t, "Europe/Berlin")

	opts := schedulerOptions(testConfig(t).Scheduler)
	if opts.Location.String() != "Europe/Berlin" {
		t.Fatalf("location = %q, want the container's zone", opts.Location)
	}
}

func TestSchedulerOptions_ExplicitTimezoneWins(t *testing.T) {
	time.Local = mustLoadLocation(t, "Europe/Berlin")

	opts := schedulerOptions(testConfig(t, "SCHEDULE_TIMEZONE=America/New_York").Scheduler)
	if opts.Location.String() != "America/New_York" {
		t.Fatalf("location = %q, want the configured zone", opts.Location)
	}
}

func mustLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	original := time.Local
	t.Cleanup(func() { time.Local = original })

	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("timezone %s unavailable: %v", name, err)
	}
	return loc
}

// The defaults config declares are the ones the packages fall back to, so the
// generated reference tells the truth about an unset variable.
func TestConfigDefaultsMatchThePackages(t *testing.T) {
	cfg := testConfig(t)
	opts := schedulerOptions(cfg.Scheduler)
	if opts.MaxConcurrent != scheduler.DefaultMaxConcurrent {
		t.Errorf("SCHEDULER_MAX_CONCURRENT default = %d, scheduler's = %d",
			opts.MaxConcurrent, scheduler.DefaultMaxConcurrent)
	}
	if opts.HistoryWindow != jobs.DefaultHistoryWindow {
		t.Errorf("JOB_HISTORY_DAYS default = %v, jobs' = %v", opts.HistoryWindow, jobs.DefaultHistoryWindow)
	}
	if opts.PruneInterval != scheduler.DefaultPruneInterval {
		t.Errorf("PRUNE_INTERVAL_HOURS default = %v, scheduler's = %v",
			opts.PruneInterval, scheduler.DefaultPruneInterval)
	}
	if cfg.OpenCloud.AdminAppRoleID != api.DefaultAdminAppRoleID {
		t.Errorf("OC_ADMIN_APP_ROLE_ID default = %q, api's = %q",
			cfg.OpenCloud.AdminAppRoleID, api.DefaultAdminAppRoleID)
	}
	if cfg.State.Prefix != cs3state.DefaultPrefix {
		t.Errorf("STATE_PREFIX default = %q, cs3state's = %q", cfg.State.Prefix, cs3state.DefaultPrefix)
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

	failing := startupDeps{openState: func(config.Backupd, *cs3.Client, *slog.Logger) (state.Store, error) {
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
	// Which build is running comes first (compatibility-policy.md §1).
	if !strings.Contains(logs.String(), `"msg":"starting backupd","version":"`+buildinfo.DevVersion+`"`) {
		t.Errorf("no version line in %s", logs.String())
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
