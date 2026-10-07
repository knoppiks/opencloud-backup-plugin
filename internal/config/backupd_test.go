package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// wrapKey returns a fresh, valid wrapping key. Generated at runtime: a
// key-shaped literal in a test is indistinguishable from a leaked one.
func wrapKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func mustLoad(t *testing.T, environ ...string) Backupd {
	t.Helper()
	c, err := LoadBackupd(environ)
	if err != nil {
		t.Fatalf("LoadBackupd(%v): %v", environ, err)
	}
	return c
}

func loadErr(t *testing.T, environ ...string) error {
	t.Helper()
	_, err := LoadBackupd(environ)
	if err == nil {
		t.Fatalf("LoadBackupd(%v) succeeded, want an error", environ)
	}
	return err
}

func TestLoadBackupd_EmptyEnvironmentGivesTheDefaults(t *testing.T) {
	c := mustLoad(t)
	if c.HTTP.Addr != ":8080" {
		t.Errorf("Addr = %q", c.HTTP.Addr)
	}
	if c.State.Prefix != ".backup-service-state" {
		t.Errorf("State.Prefix = %q", c.State.Prefix)
	}
	if c.Scheduler.MaxConcurrent != 2 || c.Scheduler.JobHistoryDays != 365 || c.Scheduler.PruneIntervalHours != 24 {
		t.Errorf("Scheduler = %+v", c.Scheduler)
	}
	if c.OpenCloud.AdminAppRoleID == "" {
		t.Error("AdminAppRoleID has no default")
	}
	if c.Backup.WorkDirAllowDisk || c.Bootstrap.Enable {
		t.Error("a boolean defaulted to true")
	}
	if c.Keys.SRW.IsSet() || c.CS3.ServiceAccountSecret.IsSet() {
		t.Error("an unset secret reads as set")
	}
	if c.Scheduler.Location() != time.Local {
		t.Errorf("Location = %v, want the container's zone", c.Scheduler.Location())
	}
}

func TestLoadBackupd_ReadsValues(t *testing.T) {
	c := mustLoad(t,
		"BACKUPD_ADDR=:9090",
		"OC_BASE_URL= https://cloud.example ",
		"ADMIN_SUBJECT_ALLOWLIST=a, b,,c ",
		"BACKUP_PARALLELISM=4",
		"BACKUP_WORK_DIR_ALLOW_DISK=true",
		"SMTP_PASSWORD= keeps its spaces ",
		"SCHEDULE_TIMEZONE=Europe/Berlin",
	)
	if c.HTTP.Addr != ":9090" {
		t.Errorf("Addr = %q", c.HTTP.Addr)
	}
	if c.OpenCloud.BaseURL != "https://cloud.example" {
		t.Errorf("BaseURL = %q, want it trimmed", c.OpenCloud.BaseURL)
	}
	if !reflect.DeepEqual(c.OpenCloud.AdminAllowlist, []string{"a", "b", "c"}) {
		t.Errorf("AdminAllowlist = %q", c.OpenCloud.AdminAllowlist)
	}
	if c.Backup.Parallelism != 4 || !c.Backup.WorkDirAllowDisk {
		t.Errorf("Backup = %+v", c.Backup)
	}
	// A password may legitimately start or end with a space; a secret is
	// taken as given.
	if c.SMTP.Password.Reveal() != " keeps its spaces " {
		t.Errorf("SMTP password was altered")
	}
	if c.Scheduler.Location().String() != "Europe/Berlin" {
		t.Errorf("Location = %v", c.Scheduler.Location())
	}
}

// os.Getenv returns the first of duplicate entries; so does the loader, so a
// value is the same whichever way it is read.
func TestLoadBackupd_FirstDuplicateWins(t *testing.T) {
	c := mustLoad(t, "BACKUPD_ADDR=:1", "BACKUPD_ADDR=:2")
	if c.HTTP.Addr != ":1" {
		t.Fatalf("Addr = %q, want the first entry", c.HTTP.Addr)
	}
}

// "yes" used to mean false without a word. A typo in a switch must be refused,
// not read as "off" (review-2026-10.md G4).
func TestLoadBackupd_BooleansAreStrict(t *testing.T) {
	for _, value := range []string{"yes", "no", "1", "0", "True", "TRUE", "on", "t"} {
		err := loadErr(t, "BOOTSTRAP_ENABLE="+value)
		if !strings.Contains(err.Error(), "BOOTSTRAP_ENABLE must be true or false") {
			t.Errorf("%q: err = %v", value, err)
		}
	}
	for value, want := range map[string]bool{"true": true, " true ": true, "false": false, "": false} {
		if got := mustLoad(t, "BOOTSTRAP_ENABLE="+value).Bootstrap.Enable; got != want {
			t.Errorf("%q = %v, want %v", value, got, want)
		}
	}
}

func TestLoadBackupd_IntegersAreNonNegative(t *testing.T) {
	for _, value := range []string{"-1", "abc", "1.5", "2k"} {
		err := loadErr(t, "SMTP_PORT="+value)
		if !strings.Contains(err.Error(), "SMTP_PORT must be a non-negative integer") {
			t.Errorf("%q: err = %v", value, err)
		}
	}
}

// One restart shows every problem, not one per restart.
func TestLoadBackupd_ReportsEveryProblem(t *testing.T) {
	err := loadErr(t, "SMTP_PORT=x", "BACKUP_WORK_DIR_ALLOW_DISK=yes", "TLS_CERT_FILE=/tls.crt")
	for _, name := range []string{"SMTP_PORT", "BACKUP_WORK_DIR_ALLOW_DISK", "TLS_KEY_FILE"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
}

func TestLoadBackupd_StateBackend(t *testing.T) {
	for _, value := range []string{"memory", "  Memory"} {
		if !mustLoad(t, "STATE_BACKEND="+value).State.MemoryState() {
			t.Errorf("%q is not memory state", value)
		}
	}
	if mustLoad(t).State.MemoryState() {
		t.Error("unset is memory state")
	}
	// Anything else used to mean "durable" silently; a typo of "memory" would
	// then have demanded a state Space the operator did not mean to need.
	for _, value := range []string{"cs3", "mem"} {
		if err := loadErr(t, "STATE_BACKEND="+value); !strings.Contains(err.Error(), "STATE_BACKEND must be one of memory") {
			t.Errorf("%q: err = %v", value, err)
		}
	}
}

// Half a TLS configuration is a deployment that thinks it is encrypted.
func TestLoadBackupd_TLSFilesGoTogether(t *testing.T) {
	_ = loadErr(t, "TLS_CERT_FILE=/etc/tls/tls.crt")
	_ = loadErr(t, "TLS_KEY_FILE=/etc/tls/tls.key")
	c := mustLoad(t, "TLS_CERT_FILE=/etc/tls/tls.crt", "TLS_KEY_FILE=/etc/tls/tls.key")
	if c.HTTP.TLSCertFile != "/etc/tls/tls.crt" || c.HTTP.TLSKeyFile != "/etc/tls/tls.key" {
		t.Fatalf("HTTP = %+v", c.HTTP)
	}
}

func TestLoadBackupd_OIDCNeedsAudienceAndOpenCloud(t *testing.T) {
	err := loadErr(t, "OIDC_ISSUER=https://issuer.invalid")
	for _, name := range []string{"OIDC_AUDIENCE", "OC_BASE_URL"} {
		if !strings.Contains(err.Error(), name+" is required") {
			t.Errorf("error does not require %s: %v", name, err)
		}
	}
	mustLoad(t, "OIDC_ISSUER=https://issuer.invalid", "OIDC_AUDIENCE=web", "OC_BASE_URL=https://cloud.example")
}

// The service used to start without the service account and fail on its
// first gateway call; only the provisioning command checked.
func TestLoadBackupd_CS3NeedsTheServiceAccount(t *testing.T) {
	for name, environ := range map[string][]string{
		"neither":    {"CS3_GATEWAY_ADDR=gw:9142"},
		"no secret":  {"CS3_GATEWAY_ADDR=gw:9142", "OC_SERVICE_ACCOUNT_ID=svc"},
		"no account": {"CS3_GATEWAY_ADDR=gw:9142", "OC_SERVICE_ACCOUNT_SECRET=s"},
	} {
		_, err := LoadBackupd(environ)
		if err == nil || !strings.Contains(err.Error(), "OC_SERVICE_ACCOUNT_ID and OC_SERVICE_ACCOUNT_SECRET are required") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	mustLoad(t, "CS3_GATEWAY_ADDR=gw:9142", "OC_SERVICE_ACCOUNT_ID=svc", "OC_SERVICE_ACCOUNT_SECRET=s")
	// Without a gateway nothing dials, so nothing is required.
	mustLoad(t)
}

func TestLoadBackupd_RejectsAnUnknownTimezone(t *testing.T) {
	err := loadErr(t, "SCHEDULE_TIMEZONE=Middle/Earth")
	if !strings.Contains(err.Error(), "SCHEDULE_TIMEZONE") {
		t.Fatalf("err = %v", err)
	}
}

// The base path is a path, not an origin. The browser client refuses an
// absolute URL on its side for the same reason: the extension and this service
// must share an origin, because nothing here implements CORS.
func TestLoadBackupd_BasePath(t *testing.T) {
	cases := map[string]struct {
		set     string
		want    string
		wantErr string
	}{
		"unset":                {set: "", want: ""},
		"prefix":               {set: "/backup", want: "/backup"},
		"trailing slash":       {set: "/backup/", want: "/backup"},
		"nested":               {set: "/apps/backup", want: "/apps/backup"},
		"whitespace":           {set: "  /backup  ", want: "/backup"},
		"root means unset":     {set: "/", want: ""},
		"double root is unset": {set: "//", want: ""},

		"absolute URL":       {set: "https://backup.example.org/api", wantErr: "not a URL"},
		"scheme relative":    {set: "//backup.example.org/api", wantErr: "clean path"},
		"no leading slash":   {set: "backup", wantErr: "must start with a slash"},
		"query":              {set: "/backup?x=1", wantErr: "not a URL"},
		"fragment":           {set: "/backup#x", wantErr: "not a URL"},
		"empty segment":      {set: "/backup//api", wantErr: "clean path"},
		"relative segment":   {set: "/backup/../etc", wantErr: "clean path"},
		"shadows healthz":    {set: "/healthz", wantErr: "health probes"},
		"shadows readyz":     {set: "/readyz", wantErr: "health probes"},
		"shadows with slash": {set: "/readyz/", wantErr: "health probes"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := LoadBackupd([]string{"BACKUPD_BASE_PATH=" + tc.set})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.HTTP.BasePath != tc.want {
				t.Fatalf("BasePath = %q, want %q", c.HTTP.BasePath, tc.want)
			}
		})
	}
}

func TestLoadBackupd_RejectsOneKeyInBothRoles(t *testing.T) {
	same := wrapKey(t)
	err := loadErr(t, "SRW_KEY="+same, "TW_KEY="+same)
	if !strings.Contains(err.Error(), "must be different") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), same) {
		t.Fatal("the error carries key material")
	}
	mustLoad(t, "SRW_KEY="+wrapKey(t), "TW_KEY="+wrapKey(t))
}

func TestLoadBackupd_RejectsMalformedKeys(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	for _, env := range []string{"SRW_KEY", "SRW_KEY_OLD", "TW_KEY", "TW_KEY_OLD"} {
		for value, want := range map[string]string{
			"not base64!": "base64-encoded",
			short:         "32 bytes",
		} {
			err := loadErr(t, env+"="+value)
			if !strings.Contains(err.Error(), env+" must") || !strings.Contains(err.Error(), want) {
				t.Errorf("%s=%q: err = %v", env, value, err)
			}
			if strings.Contains(err.Error(), value) {
				t.Errorf("%s: the error repeats the value", env)
			}
		}
	}
}

func TestDecodeWrapKey(t *testing.T) {
	if key, err := DecodeWrapKey("SRW_KEY", Secret{}); key != nil || err != nil {
		t.Fatalf("unset = %v, %v; want nil, nil", key, err)
	}
	raw := wrapKey(t)
	key, err := DecodeWrapKey("SRW_KEY", NewSecret(" "+raw+"\n"))
	if err != nil || base64.StdEncoding.EncodeToString(key) != raw {
		t.Fatalf("DecodeWrapKey = %v", err)
	}
}

// The unedited manifest is refused, and the list of variables checked is the
// struct, not a list somebody has to remember to extend.
func TestLoadBackupd_RefusesPlaceholdersInEveryVariable(t *testing.T) {
	for _, v := range variables(reflect.ValueOf(&Backupd{}).Elem()) {
		value := "REPLACE_ME_" + strings.ToLower(v.env)
		_, err := LoadBackupd([]string{v.env + "=" + value})
		if !errors.Is(err, ErrPlaceholder) || !strings.Contains(err.Error(), v.env) {
			t.Errorf("%s: err = %v, want the placeholder refusal naming it", v.env, err)
		}
		if err != nil && strings.Contains(err.Error(), value) {
			t.Errorf("%s: the error repeats the value", v.env)
		}
	}
}

func TestLoadBackupd_PlaceholdersAreReportedTogetherAndAlone(t *testing.T) {
	err := loadErr(t,
		"STATE_SPACE_ID=REPLACE_ME_WITH_THE_STATE_SPACE_ID",
		"OIDC_AUDIENCE=REPLACE_ME_WITH_THE_OIDC_CLIENT_ID",
		"SMTP_PORT=not-a-port",
	)
	if !errors.Is(err, ErrPlaceholder) {
		t.Fatalf("err = %v", err)
	}
	for _, name := range []string{"STATE_SPACE_ID", "OIDC_AUDIENCE"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s", name)
		}
	}
	// Every diagnosis after an unfinished manifest is of a symptom.
	if strings.Contains(err.Error(), "SMTP_PORT") {
		t.Errorf("the placeholder error is buried among others: %v", err)
	}
}

// Somebody else's placeholder is somebody else's problem: refusing to start
// over a variable this service never reads would be a surprise with no fix
// inside this deployment.
func TestLoadBackupd_IgnoresForeignPlaceholders(t *testing.T) {
	mustLoad(t, "SOME_OTHER_CHART_TOKEN=REPLACE_ME", "BACKUP_SOMETHING_ELSE=REPLACE_ME")
}
