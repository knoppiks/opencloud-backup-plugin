package main

// The admin Take-Out tool must be structurally incapable of decrypting.
// These tests are an audit of that property, not just of behaviour: they fail
// if someone ever adds a key input to this binary (decisions.md #2, #15).

import (
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
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
		"snapshot.Repo{", "RestoreAll", "takeout/decrypt",
	} {
		if strings.Contains(string(src), forbidden) {
			t.Errorf("takeout must not reference %s", forbidden)
		}
	}
}

// forbiddenDeps are packages that must never be linked into this binary. Reading
// the source (above) catches the obvious mistake; this catches the indirect one,
// where a package this binary already imports grows an import of its own.
//
// pkg/takeout/decrypt exists as a separate package for exactly this reason: the
// admin's tool is built without the code that unwraps a Data Key and restores a
// repository, rather than merely never calling it (decisions.md #2, #15).
//
// pkg/keys is deliberately *not* on this list: extraction reads an envelope's
// public header, so the package is linked. That its unwrap functions stay
// unreferenced is what the source audit above checks.
var forbiddenDeps = []string{
	"opencloud-backup-plugin/pkg/takeout/decrypt",
	"opencloud-backup-plugin/pkg/restore",
}

func TestBinaryDoesNotLinkTheDecryptPath(t *testing.T) {
	deps := packageDeps(t, ".")
	for _, forbidden := range forbiddenDeps {
		if deps[forbidden] {
			t.Errorf("takeout links %s; the admin's tool must not be built with the ability to decrypt", forbidden)
		}
	}

	// A control: the check above must be failing for the right reason. If the
	// decrypt package were renamed or removed, the loop would pass vacuously.
	userSide := packageDeps(t, "../decrypt")
	if !userSide["opencloud-backup-plugin/pkg/takeout/decrypt"] {
		t.Fatal("the user-side decrypt CLI no longer links pkg/takeout/decrypt; " +
			"the forbidden-dependency list above is now checking nothing")
	}
}

// packageDeps returns the full transitive dependency set of a package.
func packageDeps(t *testing.T, pkg string) map[string]bool {
	t.Helper()

	goBin, err := exec.LookPath("go")
	if err != nil {
		// The source audit still applies; only the structural check is lost.
		t.Skipf("go toolchain not on PATH: %v", err)
	}

	out, err := exec.Command(goBin, "list", "-deps", pkg).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	deps := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		deps[line] = true
	}
	return deps
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
