package main

import (
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"

	"opencloud-backup-plugin/internal/config"
	"opencloud-backup-plugin/internal/config/configtest"
	"opencloud-backup-plugin/pkg/cs3state"
)

func loadErr(t *testing.T, environ ...string) error {
	t.Helper()
	_, err := loadServiceEnv(environ)
	if err == nil {
		t.Fatalf("loadServiceEnv(%v) succeeded, want an error", environ)
	}
	return err
}

// Documented, declared once, credentials redacted by type, placeholders
// refused in every variable.
func TestServiceEnv_Declarations(t *testing.T) {
	configtest.CheckDeclarations(t, func() any {
		env := newServiceEnv()
		return &env
	})
}

// The checked-in reference is what the declarations generate. When this
// fails, run make generate and commit the result.
func TestEnvironmentReference(t *testing.T) {
	env := newServiceEnv()
	configtest.CheckReference(t, config.Program{
		Name: "backupd",
		Intro: "The backup service, and its operator commands (`provision-state-space`, " +
			"`rotate-srw`, `rotate-tw`), which read the same configuration.\n\n" +
			"The Go runtime also reads `TZ` (the zone schedules are read in, unless " +
			"`SCHEDULE_TIMEZONE` says otherwise) and `SSL_CERT_DIR` / `SSL_CERT_FILE` " +
			"(extra CAs, for example for a backup target with a private certificate).",
		Config:    &env,
		Generator: "make generate",
	}, filepath.Join("..", "..", "docs", "reference", "environment-backupd.md"))
}

func TestServiceEnv_EmptyEnvironmentGivesTheDefaults(t *testing.T) {
	c := testConfig(t)
	if c.HTTP.Addr != ":8080" {
		t.Errorf("Addr = %q", c.HTTP.Addr)
	}
	if c.State.Prefix != cs3state.DefaultPrefix || c.State.memory() {
		t.Errorf("State = %+v", c.State)
	}
	if c.Backup.WorkDirAllowDisk || c.Bootstrap.Enable {
		t.Error("a boolean defaulted to true")
	}
	if c.Keys.SRW.IsSet() || c.CS3.ServiceAccountSecret.IsSet() {
		t.Error("an unset secret reads as set")
	}
}

// One restart shows every problem, across every component.
func TestServiceEnv_ReportsEveryProblem(t *testing.T) {
	err := loadErr(t,
		"SMTP_PORT=x",                     // notify
		"BACKUP_WORK_DIR_ALLOW_DISK=yes",  // this binary
		"TLS_CERT_FILE=/tls.crt",          // this binary, a check
		"OIDC_ISSUER=https://issuer.test", // api, a check
		"CS3_GATEWAY_ADDR=gw:9142",        // cs3, a check
		"SCHEDULE_TIMEZONE=Middle/Earth",  // scheduler, a check
	)
	for _, name := range []string{
		"SMTP_PORT", "BACKUP_WORK_DIR_ALLOW_DISK", "TLS_KEY_FILE", "OIDC_AUDIENCE",
		"OC_SERVICE_ACCOUNT_SECRET", "SCHEDULE_TIMEZONE",
	} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
}

func TestServiceEnv_StateBackend(t *testing.T) {
	for _, value := range []string{"memory", "  Memory"} {
		if !testConfig(t, "STATE_BACKEND="+value).State.memory() {
			t.Errorf("%q is not memory state", value)
		}
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
func TestServiceEnv_TLSFilesGoTogether(t *testing.T) {
	_ = loadErr(t, "TLS_CERT_FILE=/etc/tls/tls.crt")
	_ = loadErr(t, "TLS_KEY_FILE=/etc/tls/tls.key")
	c := testConfig(t, "TLS_CERT_FILE=/etc/tls/tls.crt", "TLS_KEY_FILE=/etc/tls/tls.key")
	if c.HTTP.TLSCertFile != "/etc/tls/tls.crt" || c.HTTP.TLSKeyFile != "/etc/tls/tls.key" {
		t.Fatalf("HTTP = %+v", c.HTTP)
	}
}

// The base path is a path, not an origin. The browser client refuses an
// absolute URL on its side for the same reason: the extension and this service
// must share an origin, because nothing here implements CORS.
func TestServiceEnv_BasePath(t *testing.T) {
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
			c, err := loadServiceEnv([]string{"BACKUPD_BASE_PATH=" + tc.set})
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

func TestServiceEnv_RejectsOneKeyInBothRoles(t *testing.T) {
	same := encodedKey(t)
	err := loadErr(t, "SRW_KEY="+same, "TW_KEY="+same)
	if !strings.Contains(err.Error(), "must be different") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), same) {
		t.Fatal("the error carries key material")
	}
	testConfig(t, "SRW_KEY="+encodedKey(t), "TW_KEY="+encodedKey(t))
}

func TestServiceEnv_RejectsMalformedKeys(t *testing.T) {
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
	if key, err := decodeWrapKey("SRW_KEY", config.Secret{}); key != nil || err != nil {
		t.Fatalf("unset = %v, %v; want nil, nil", key, err)
	}
	raw := encodedKey(t)
	key, err := decodeWrapKey("SRW_KEY", config.NewSecret(" "+raw+"\n"))
	if err != nil || base64.StdEncoding.EncodeToString(key) != raw {
		t.Fatalf("decodeWrapKey = %v", err)
	}
}

// Seeding seals the target's credentials with the TW key; without it the
// seeding used to be skipped without a word.
func TestServiceEnv_SeedingNeedsTheTargetWrapKey(t *testing.T) {
	seed := []string{
		"BOOTSTRAP_ENABLE=true", "BOOTSTRAP_S3_ENDPOINT=s3.test:3900", "BOOTSTRAP_S3_BUCKET=b",
		"BOOTSTRAP_S3_ACCESS_KEY_ID=id", "BOOTSTRAP_S3_SECRET_ACCESS_KEY=s",
	}
	if err := loadErr(t, seed...); !strings.Contains(err.Error(), "TW_KEY is unset") {
		t.Fatalf("err = %v", err)
	}
	testConfig(t, append(seed, "TW_KEY="+encodedKey(t))...)
}

// An enabled seeding with nothing to seed is refused at startup now, before
// the instance record is claimed (targets.BootstrapEnv).
func TestServiceEnv_SeedingNeedsATarget(t *testing.T) {
	err := loadErr(t, "BOOTSTRAP_ENABLE=true", "TW_KEY="+encodedKey(t))
	if !strings.Contains(err.Error(), "BOOTSTRAP_S3_* target is incomplete") {
		t.Fatalf("err = %v", err)
	}
}
