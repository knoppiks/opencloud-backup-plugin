package main

// The decrypt CLI is the last-resort recovery tool. What is tested here is what
// a stressed user actually depends on: the key is never a command-line argument,
// a mistyped key says so, and failures are explained in plain language.

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"testing/iotest"

	"opencloud-backup-plugin/internal/buildinfo"
	"opencloud-backup-plugin/internal/cli"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/takeout"
	takeoutdecrypt "opencloud-backup-plugin/pkg/takeout/decrypt"
)

// pipeStdin feeds text to readRecoveryKey as a non-terminal stdin.
func pipeStdin(t *testing.T, text string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	go func() {
		_, _ = io.WriteString(w, text)
		_ = w.Close()
	}()
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestReadRecoveryKeyFromPipedInput(t *testing.T) {
	display, secret, err := keys.GenerateRecoveryKey()
	if err != nil {
		t.Fatalf("GenerateRecoveryKey: %v", err)
	}

	got, err := readRecoveryKey(pipeStdin(t, display+"\n"), io.Discard)
	if err != nil {
		t.Fatalf("readRecoveryKey: %v", err)
	}
	if string(got) != string(secret) {
		t.Fatal("decoded recovery key does not match the generated one")
	}
}

// Users retype these by hand from a password manager.
func TestReadRecoveryKeyToleratesUserTypingAndRejectsTypos(t *testing.T) {
	display, secret, err := keys.GenerateRecoveryKey()
	if err != nil {
		t.Fatalf("GenerateRecoveryKey: %v", err)
	}

	messy := "  " + strings.ToLower(display) + "  \n"
	got, err := readRecoveryKey(pipeStdin(t, messy), io.Discard)
	if err != nil {
		t.Fatalf("readRecoveryKey(lowercase, padded): %v", err)
	}
	if string(got) != string(secret) {
		t.Fatal("case/whitespace-tolerant decoding failed")
	}

	// Flip one payload character: the checksum must catch it before any
	// expensive KDF runs. The *first* payload character is mutated on purpose —
	// the last one carries padding bits, so not every change to it alters the
	// decoded bytes.
	broken := []byte(display)
	first := strings.IndexByte(display, '-') + 1
	if broken[first] == 'A' {
		broken[first] = 'B'
	} else {
		broken[first] = 'A'
	}
	_, err = readRecoveryKey(pipeStdin(t, string(broken)+"\n"), io.Discard)
	if err == nil {
		t.Fatal("a mistyped recovery key must be rejected")
	}
	if !strings.Contains(err.Error(), "ocbk1-") {
		t.Fatalf("error should show the expected shape, got %q", err)
	}
}

func TestReadRecoveryKeyRejectsEmptyInput(t *testing.T) {
	if _, err := readRecoveryKey(pipeStdin(t, "\n"), io.Discard); err == nil {
		t.Fatal("an empty recovery key must be rejected")
	}
}

// The Recovery Key must never be accepted as an argument: command lines are
// world-readable and end up in shell history.
func TestRecoveryKeyIsNotAFlag(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	text := string(src)

	for _, forbidden := range []string{`"key"`, `"rk"`, `"recovery-key"`, `"password"`, `"passphrase"`} {
		if strings.Contains(text, "fs.StringVar(&cfg") && strings.Contains(text, forbidden+",") {
			t.Errorf("recovery key must not be a flag (%s)", forbidden)
		}
	}
	if !strings.Contains(text, "term.ReadPassword") {
		t.Error("interactive input must be read without echo")
	}
}

func TestReadRecoveryKeyFromAnyReader(t *testing.T) {
	display, secret, err := keys.GenerateRecoveryKey()
	if err != nil {
		t.Fatalf("GenerateRecoveryKey: %v", err)
	}
	// No trailing newline: a script may pipe the key without one.
	got, err := readRecoveryKey(strings.NewReader(display), io.Discard)
	if err != nil {
		t.Fatalf("readRecoveryKey: %v", err)
	}
	if string(got) != string(secret) {
		t.Fatal("decoded recovery key does not match the generated one")
	}
}

func TestReadRecoveryKeyReportsAReadFailure(t *testing.T) {
	_, err := readRecoveryKey(iotest.ErrReader(errors.New("stdin closed")), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "could not read the recovery key") {
		t.Fatalf("err = %v", err)
	}
}

// Wrong usage is exit status 2, and says what was wrong.
func TestRunRequiresInputAndOutput(t *testing.T) {
	r := runDecrypt(t, "")
	if r.code != cli.ExitUsage || !strings.Contains(r.stderr, "-in is required") {
		t.Fatalf("run = %d, stderr %q", r.code, r.stderr)
	}
	r = runDecrypt(t, "", "-in", t.TempDir())
	if r.code != cli.ExitUsage || !strings.Contains(r.stderr, "-out is required") {
		t.Fatalf("run = %d, stderr %q", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "Run 'decrypt -h' for usage.") {
		t.Fatalf("stderr = %q, want a pointer to the help", r.stderr)
	}
}

func TestRunRejectsUnknownFlagsAndStrayArguments(t *testing.T) {
	for _, args := range [][]string{
		{"-in", "x", "-out", "y", "-recovery-key", "z"},
		{"-in", "x", "-out", "y", "extra"},
	} {
		r := runDecrypt(t, "", args...)
		if r.code != cli.ExitUsage {
			t.Errorf("%v: run = %d, want %d (stderr %q)", args, r.code, cli.ExitUsage, r.stderr)
		}
		if r.stdout != "" {
			t.Errorf("%v: stdout = %q", args, r.stdout)
		}
	}
}

// Help is not an error: exit 0, the text on stdout.
func TestHelpExitsZero(t *testing.T) {
	r := runDecrypt(t, "", "-h")
	if r.code != cli.ExitOK {
		t.Fatalf("run -h = %d", r.code)
	}
	for _, want := range []string{"restore your files from a take-out", "-envelope", "Exit status"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("help does not mention %q:\n%s", want, r.stdout)
		}
	}
}

func TestVersion(t *testing.T) {
	r := runDecrypt(t, "", "-version")
	if r.code != cli.ExitOK || !strings.HasPrefix(r.stdout, "decrypt "+buildinfo.DevVersion+" (") {
		t.Fatalf("run -version = %d, stdout %q", r.code, r.stdout)
	}
}

// -verify needs no key, so it must not prompt for one.
func TestVerifyOnNonTakeOutDirectory(t *testing.T) {
	r := runDecrypt(t, "", "-in", t.TempDir(), "-verify")
	if r.code != cli.ExitFailure || !strings.Contains(r.stderr, "not a take-out") {
		t.Fatalf("run = %d, stderr %q, want the not-a-take-out explanation", r.code, r.stderr)
	}
	if strings.Contains(r.stderr, "Recovery Key") {
		t.Fatal("-verify prompted for a key")
	}
}

func TestExplainSpeaksPlainly(t *testing.T) {
	cases := map[error]string{
		takeoutdecrypt.ErrWrongRecoveryKey:     "key does not match",
		takeout.ErrNoTakeOut:                   "not a take-out",
		takeout.ErrNoEnvelope:                  "cannot be decrypted",
		takeoutdecrypt.ErrUnsupportedEnvelope:  "newer",
		takeoutdecrypt.ErrBadEnvelopeFile:      "-envelope",
		takeoutdecrypt.ErrEnvelopeFileMismatch: "same space",
		takeout.ErrCorrupt:                     "damaged",
		takeout.ErrNewerTakeOut:                "newer 'decrypt'",
		snapshot.ErrSnapshotNotFound:           "-list",
	}
	for in, want := range cases {
		got := explain(in)
		if got == nil || !strings.Contains(got.Error(), want) {
			t.Errorf("explain(%v) = %v, want it to mention %q", in, got, want)
		}
	}

	other := errors.New("disk full")
	if got := explain(other); !errors.Is(got, other) {
		t.Errorf("explain must pass unknown errors through, got %v", got)
	}
}

func TestEnvelopeFlagReachesTheLibrary(t *testing.T) {
	cfg := config{in: "in", out: "out", snapshotID: "s", workDir: "w", envelope: "e"}
	opts := cfg.options([]byte{1})
	if opts.Dir != "in" || opts.OutDir != "out" || opts.SnapshotID != "s" ||
		opts.WorkDir != "w" || opts.EnvelopeFile != "e" || len(opts.RecoveryKey) != 1 {
		t.Fatalf("options = %+v", opts)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:               "0 B",
		999:             "999 B",
		1024:            "1.0 KiB",
		5 * 1024 * 1024: "5.0 MiB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
