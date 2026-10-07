package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"strings"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/keys"
)

func encodedKey(t *testing.T) string {
	t.Helper()
	key, err := keys.GenerateSRWKey()
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

// The two custody keys are separate so they can rotate independently. Setting
// them to one value works perfectly and quietly halves the trust model, so it is
// refused at boot rather than discovered during an incident.
func TestLoadWrapKeysRejectsOneKeyInBothRoles(t *testing.T) {
	same := encodedKey(t)
	t.Setenv("SRW_KEY", same)
	t.Setenv("TW_KEY", same)

	_, err := loadWrapKeys()
	if err == nil {
		t.Fatal("the same key was accepted for both SRW and TW")
	}
	if !strings.Contains(err.Error(), "must be different") {
		t.Fatalf("err = %v, want an explanation of what to fix", err)
	}
	// The error names the variables, never their values.
	if strings.Contains(err.Error(), same) {
		t.Fatal("the error message leaked key material")
	}
}

func TestLoadWrapKeysAcceptsDistinctKeys(t *testing.T) {
	srw, tw := encodedKey(t), encodedKey(t)
	t.Setenv("SRW_KEY", srw)
	t.Setenv("TW_KEY", tw)

	loaded, err := loadWrapKeys()
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
}

// Either key may be absent — that disables a feature, it is not an error — and
// two absent keys are not "the same key".
func TestLoadWrapKeysToleratesUnsetKeys(t *testing.T) {
	for name, env := range map[string]struct{ srw, tw string }{
		"neither set": {},
		"only srw":    {srw: encodedKey(t)},
		"only tw":     {tw: encodedKey(t)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SRW_KEY", env.srw)
			t.Setenv("TW_KEY", env.tw)

			loaded, err := loadWrapKeys()
			if err != nil {
				t.Fatalf("loadWrapKeys: %v", err)
			}
			defer loaded.zeroize()

			if (env.srw == "") != (loaded.srw == nil) {
				t.Fatalf("SRW key presence = %v, want %v", loaded.srw != nil, env.srw != "")
			}
			if (env.tw == "") != (loaded.tw == nil) {
				t.Fatalf("TW key presence = %v, want %v", loaded.tw != nil, env.tw != "")
			}
		})
	}
}

func TestLoadWrapKeyRejectsMalformedValues(t *testing.T) {
	for name, value := range map[string]string{
		"not base64": "!!!not base64!!!",
		"too short":  base64.StdEncoding.EncodeToString([]byte("short")),
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SRW_KEY", value)
			if _, err := loadWrapKey("SRW_KEY"); err == nil {
				t.Fatal("a malformed key was accepted")
			} else if strings.Contains(err.Error(), value) {
				t.Fatal("the error message echoed the configured value")
			}
		})
	}
}

// "Nightly at half past two" is about the family's night. The container's own
// zone is the closest thing this process can know, and a deployment that sets
// TZ has already said which zone it thinks in.
func TestSchedulerOptions_DefaultsToTheContainerTimezone(t *testing.T) {
	t.Setenv("TZ", "Europe/Berlin")
	// Go reads TZ once, at first use; force the re-read this test depends on.
	time.Local = mustLoad(t, "Europe/Berlin")

	opts, err := schedulerOptions()
	if err != nil {
		t.Fatalf("schedulerOptions: %v", err)
	}
	if opts.Location.String() != "Europe/Berlin" {
		t.Fatalf("location = %q, want the container's zone", opts.Location)
	}
}

func TestSchedulerOptions_ExplicitTimezoneWins(t *testing.T) {
	time.Local = mustLoad(t, "Europe/Berlin")
	t.Setenv("SCHEDULE_TIMEZONE", "America/New_York")

	opts, err := schedulerOptions()
	if err != nil {
		t.Fatalf("schedulerOptions: %v", err)
	}
	if opts.Location.String() != "America/New_York" {
		t.Fatalf("location = %q, want the configured zone", opts.Location)
	}
}

func TestSchedulerOptions_RejectsAnUnknownTimezone(t *testing.T) {
	t.Setenv("SCHEDULE_TIMEZONE", "Middle/Earth")

	if _, err := schedulerOptions(); err == nil {
		t.Fatal("an unknown timezone must be refused at startup, not at 02:30")
	}
}

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	original := time.Local
	t.Cleanup(func() { time.Local = original })

	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("timezone %s unavailable: %v", name, err)
	}
	return loc
}

// A service that authenticates tokens but cannot find out whose OpenCloud user
// they belong to would answer 503 to everyone. It refuses to start instead.
func TestBuildService_RequiresOCBaseURLWithOIDC(t *testing.T) {
	t.Setenv("BACKUP_WORK_DIR_ALLOW_DISK", "true")
	t.Setenv("BACKUP_WORK_DIR", t.TempDir())
	t.Setenv("OIDC_ISSUER", "https://issuer.invalid")
	t.Setenv("OIDC_AUDIENCE", "web")
	t.Setenv("OC_BASE_URL", "")

	_, cleanup, err := buildService(context.Background(), discardLogger(), productionStartup())
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "OC_BASE_URL is required") {
		t.Fatalf("err = %v, want a refusal naming OC_BASE_URL", err)
	}
}

func TestBuildOpenCloudMonitor(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)

	t.Setenv("OC_BASE_URL", "")
	m, err := buildOpenCloudMonitor(logger)
	if err != nil || m != nil {
		t.Fatalf("without OC_BASE_URL: monitor %v, err %v; want neither", m, err)
	}

	t.Setenv("OC_BASE_URL", "https://cloud.example")
	m, err = buildOpenCloudMonitor(logger)
	if err != nil || m == nil {
		t.Fatalf("with OC_BASE_URL: monitor %v, err %v", m, err)
	}
	// Nothing is fetched until serve runs it, so a down OpenCloud cannot
	// delay startup.
	if st := m.Status(); st.Known || st.Window.String() == "" {
		t.Errorf("fresh monitor status = %+v", st)
	}
}
