package main

import (
	"bytes"
	"encoding/base64"
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
