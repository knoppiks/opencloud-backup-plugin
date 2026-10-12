package main

// The admin Take-Out tool must be structurally incapable of decrypting.
// These tests are an audit of that property, not just of behaviour: they fail
// if someone ever adds a key input to this binary (decisions.md #2, #15).

import (
	"errors"
	"flag"
	"os"
	"strings"
	"testing"

	"opencloud-backup-plugin/internal/cli"
	"opencloud-backup-plugin/internal/testutil"
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
	fs := flags(&cfg)

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
	deps := testutil.PackageDeps(t, ".")
	for _, forbidden := range forbiddenDeps {
		if deps[forbidden] {
			t.Errorf("takeout links %s; the admin's tool must not be built with the ability to decrypt", forbidden)
		}
	}

	// A control: the check above must be failing for the right reason. If the
	// decrypt package were renamed or removed, the loop would pass vacuously.
	userSide := testutil.PackageDeps(t, "../decrypt")
	if !userSide["opencloud-backup-plugin/pkg/takeout/decrypt"] {
		t.Fatal("the user-side decrypt CLI no longer links pkg/takeout/decrypt; " +
			"the forbidden-dependency list above is now checking nothing")
	}
}

func TestRequireFlagsNamesWhatIsMissing(t *testing.T) {
	cases := map[string]struct {
		cfg  config
		want string
	}{
		"no bucket": {config{spaceID: "s", outDir: "o"}, "-bucket"},
		"no space":  {config{bucket: "b", outDir: "o"}, "-space"},
		"no out":    {config{bucket: "b", spaceID: "s"}, "-out"},
	}
	for name, c := range cases {
		err := requireFlags(c.cfg)
		if cli.ExitCode(err) != cli.ExitUsage || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: requireFlags = %v, want a usage error naming %s", name, err, c.want)
		}
	}
	if err := requireFlags(config{bucket: "b", spaceID: "s", outDir: "o"}); err != nil {
		t.Errorf("complete command line rejected: %v", err)
	}
}

// Missing credentials are named before anything reaches the network; the S3
// SDK would otherwise ask the cloud metadata service (review-2026-10.md F5).
func TestRequireCredentialsNamesTheMissingVariable(t *testing.T) {
	cases := map[string]struct {
		access, secret, want string
	}{
		"no access key id": {secret: "s", want: "S3_ACCESS_KEY_ID"},
		"no secret":        {access: "id", want: "S3_SECRET_ACCESS_KEY"},
		"neither":          {want: "S3_ACCESS_KEY_ID"},
	}
	for name, tc := range cases {
		err := requireCredentials(config{accessKey: tc.access, secretKey: tc.secret})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: requireCredentials = %v, want it to name %s", name, err, tc.want)
		}
		// The environment, not the command line: a failure, not wrong usage.
		if cli.ExitCode(err) != cli.ExitFailure {
			t.Errorf("%s: exit status %d, want %d", name, cli.ExitCode(err), cli.ExitFailure)
		}
	}
	if err := requireCredentials(config{accessKey: "id", secretKey: "s"}); err != nil {
		t.Errorf("credentials rejected: %v", err)
	}
}

func TestRunWithoutCredentialsNamesTheVariable(t *testing.T) {
	r := runTakeout(t, nil, unreachable, "-bucket", "b", "-space", "s", "-out", t.TempDir())
	if r.code != cli.ExitFailure || !strings.Contains(r.stderr, "S3_ACCESS_KEY_ID") {
		t.Fatalf("run = %d, stderr %q, want it to name S3_ACCESS_KEY_ID", r.code, r.stderr)
	}
}

func TestRunReportsMissingArguments(t *testing.T) {
	r := runTakeout(t, credentials(), unreachable, "-bucket", "backups")
	if r.code != cli.ExitUsage || !strings.Contains(r.stderr, "-space is required") {
		t.Fatalf("run = %d, stderr %q, want a usage error about -space", r.code, r.stderr)
	}
}

// Credentials are taken from the environment, never from the command line.
func TestCredentialsComeFromTheEnvironment(t *testing.T) {
	// The environment is read, and through the shared configuration: an
	// unedited placeholder there is refused by name before anything else.
	r := runTakeout(t, []string{"S3_ACCESS_KEY_ID=REPLACE_ME"}, unreachable,
		"-bucket", "b", "-space", "s", "-out", t.TempDir())
	if r.code != cli.ExitFailure ||
		!strings.Contains(r.stderr, "S3_ACCESS_KEY_ID still holds the manifest's placeholder") {
		t.Errorf("run = %d, stderr %q, want the placeholder in S3_ACCESS_KEY_ID refused", r.code, r.stderr)
	}

	var cfg config
	fs := flags(&cfg)
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
