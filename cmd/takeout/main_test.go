package main

// The admin Take-Out tool must be structurally incapable of decrypting.
// These tests are an audit of that property, not just of behaviour: they fail
// if someone ever adds a key input to this binary (decisions.md #2, #15).

import (
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	"opencloud-backup-plugin/pkg/takeout"
)

// keyFlagNames are the things a key input would plausibly be called.
var keyFlagNames = []string{
	"key", "rk", "dk", "secret", "password", "passphrase", "unwrap", "decrypt",
}

// keyUsagePhrases would appear in the help text of a flag that takes a secret.
var keyUsagePhrases = []string{"recovery key", "data key", "password", "passphrase", "repository key"}

func TestNoFlagAcceptsKeyMaterial(t *testing.T) {
	var cfg config
	fs := flags(&cfg, io.Discard)

	fs.VisitAll(func(f *flag.Flag) {
		name := strings.ToLower(f.Name)
		for _, word := range keyFlagNames {
			// Whole-word match on the flag name: "-key", "-recovery-key", ...
			for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' }) {
				if part == word {
					t.Errorf("flag -%s looks like a key input; this tool must never take key material", f.Name)
				}
			}
		}
		usage := strings.ToLower(f.Usage)
		for _, phrase := range keyUsagePhrases {
			if strings.Contains(usage, phrase) {
				t.Errorf("flag -%s offers to take %q", f.Name, phrase)
			}
		}
	})
}

// The source itself must not reach for the key APIs. A compile-time dependency
// on unwrapping would mean the capability exists, whatever the flags say.
func TestSourceDoesNotUseKeyUnwrappingAPIs(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	for _, forbidden := range []string{
		"UnwrapRK", "UnwrapSRW", "DecodeRecoveryKey", "GenerateDK", "keys.WrappedDK",
		"snapshot.Repo{", "RestoreAll", "takeout.Decrypt",
	} {
		if strings.Contains(string(src), forbidden) {
			t.Errorf("takeout must not reference %s", forbidden)
		}
	}
}

func TestValidateRequiresTargetAndSpace(t *testing.T) {
	cases := map[string]config{
		"no bucket": {spaceID: "s", outDir: "o"},
		"no space":  {bucket: "b", outDir: "o"},
		"no out":    {bucket: "b", spaceID: "s"},
	}
	for name, cfg := range cases {
		if err := validate(cfg); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if err := validate(config{bucket: "b", spaceID: "s", outDir: "o"}); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

func TestRunReportsMissingArguments(t *testing.T) {
	err := run([]string{"-bucket", "backups"}, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "-space") {
		t.Fatalf("err = %v, want a complaint about -space", err)
	}
}

// Credentials are taken from the environment, never from the command line.
func TestCredentialsComeFromTheEnvironment(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if !strings.Contains(string(src), `os.Getenv("S3_ACCESS_KEY_ID")`) {
		t.Error("S3 credentials must be read from the environment")
	}

	var cfg config
	fs := flags(&cfg, io.Discard)
	for _, name := range []string{"access-key", "secret-key", "secret", "access-key-id"} {
		if fs.Lookup(name) != nil {
			t.Errorf("credential flag -%s must not exist", name)
		}
	}
}

func TestExplainAddsGuidanceWithoutLosingTheCause(t *testing.T) {
	wrapped := explain(takeout.ErrNoEnvelope)
	if wrapped == nil || !strings.Contains(wrapped.Error(), "-allow-missing-envelope") {
		t.Fatalf("explain = %v, want advice about -allow-missing-envelope", wrapped)
	}

	plain := errors.New("some other failure")
	if got := explain(plain); !errors.Is(got, plain) {
		t.Fatalf("explain must pass unknown errors through, got %v", got)
	}
}
