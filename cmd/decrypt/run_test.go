package main

// The whole program, driven the way a family member drives it: a Take-Out
// folder, a Recovery Key typed (here: piped) into stdin, and what comes out on
// stdout, stderr and the exit status. The Take-Out is the frozen one
// (pkg/takeout/testdata), so this also proves the CLI, not only the library,
// opens what an earlier release wrote.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/knoppiks/opencloud-backup-plugin/internal/buildinfo"
	"github.com/knoppiks/opencloud-backup-plugin/internal/cli"
	"github.com/knoppiks/opencloud-backup-plugin/pkg/keys"
)

const frozenTakeOut = "../../pkg/takeout/testdata/takeout-v1"

// frozen is the part of the frozen Take-Out's sidecar these tests use.
type frozen struct {
	RecoveryKey string `json:"recovery_key"`
	// Snapshots are newest first.
	Snapshots []struct {
		ID    string `json:"id"`
		Files map[string]struct {
			SHA256 string `json:"sha256"`
		} `json:"files"`
	} `json:"snapshots"`
}

// openFrozen reads the sidecar and copies the Take-Out to a temporary
// directory, so no run can change the committed bytes.
func openFrozen(t *testing.T) (frozen, string) {
	t.Helper()
	data, err := os.ReadFile(frozenTakeOut + ".json")
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var meta frozen
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parse sidecar: %v", err)
	}
	if len(meta.Snapshots) < 2 || meta.RecoveryKey == "" {
		t.Fatal("the frozen Take-Out's sidecar is incomplete")
	}
	dir := filepath.Join(t.TempDir(), "takeout")
	if err := os.CopyFS(dir, os.DirFS(frozenTakeOut)); err != nil {
		t.Fatalf("copy take-out: %v", err)
	}
	return meta, dir
}

type result struct {
	code           int
	stdout, stderr string
}

// runDecrypt runs the program with stdin as the piped input.
func runDecrypt(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(stdin), &stdout, &stderr)
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func TestRunVerifiesATakeOutWithoutAKey(t *testing.T) {
	_, dir := openFrozen(t)
	r := runDecrypt(t, "", "-in", dir, "-verify")
	if r.code != cli.ExitOK || !strings.Contains(r.stdout, "matches its manifest") {
		t.Fatalf("run = %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
}

func TestRunListsSnapshots(t *testing.T) {
	meta, dir := openFrozen(t)
	r := runDecrypt(t, meta.RecoveryKey+"\n", "-in", dir, "-list", "-work-dir", t.TempDir())
	if r.code != cli.ExitOK {
		t.Fatalf("run = %d, stderr %q", r.code, r.stderr)
	}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 1+len(meta.Snapshots) || !strings.HasPrefix(lines[0], "SNAPSHOT") {
		t.Fatalf("listing:\n%s", r.stdout)
	}
	for i, snap := range meta.Snapshots {
		if !strings.HasPrefix(lines[i+1], snap.ID+" ") {
			t.Errorf("line %d = %q, want snapshot %s (newest first)", i+1, lines[i+1], snap.ID)
		}
	}
	if strings.Contains(r.stdout+r.stderr, meta.RecoveryKey) {
		t.Fatal("the Recovery Key was echoed")
	}
}

func TestRunRestoresTheNewestSnapshot(t *testing.T) {
	meta, dir := openFrozen(t)
	out := filepath.Join(t.TempDir(), "restored")
	r := runDecrypt(t, meta.RecoveryKey+"\n", "-in", dir, "-out", out, "-work-dir", t.TempDir())
	if r.code != cli.ExitOK {
		t.Fatalf("run = %d, stderr %q", r.code, r.stderr)
	}
	newest := meta.Snapshots[0]
	if !strings.Contains(r.stdout, "restored snapshot "+newest.ID) {
		t.Fatalf("stdout = %q", r.stdout)
	}
	assertRestored(t, out, newest.Files)
}

func TestRunRestoresAChosenSnapshot(t *testing.T) {
	meta, dir := openFrozen(t)
	older := meta.Snapshots[len(meta.Snapshots)-1]
	out := filepath.Join(t.TempDir(), "restored")
	r := runDecrypt(t, meta.RecoveryKey+"\n", "-in", dir, "-out", out, "-snapshot", older.ID)
	if r.code != cli.ExitOK {
		t.Fatalf("run = %d, stderr %q", r.code, r.stderr)
	}
	assertRestored(t, out, older.Files)
}

// The worst day: a wrong key. It says so plainly, exits 1, and writes nothing.
func TestRunWithTheWrongKey(t *testing.T) {
	_, dir := openFrozen(t)
	wrong, _, err := keys.GenerateRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "restored")
	r := runDecrypt(t, wrong+"\n", "-in", dir, "-out", out)
	if r.code != cli.ExitFailure || !strings.Contains(r.stderr, "key does not match") {
		t.Fatalf("run = %d, stderr %q", r.code, r.stderr)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("a wrong key left %s behind (stat: %v)", out, err)
	}
}

func TestRunWithAMistypedKey(t *testing.T) {
	_, dir := openFrozen(t)
	r := runDecrypt(t, "ocbk1-not-a-key\n", "-in", dir, "-list")
	if r.code != cli.ExitFailure || !strings.Contains(r.stderr, "ocbk1-XXXXX") {
		t.Fatalf("run = %d, stderr %q", r.code, r.stderr)
	}
}

func TestRunWithAnUnknownSnapshot(t *testing.T) {
	meta, dir := openFrozen(t)
	r := runDecrypt(t, meta.RecoveryKey+"\n",
		"-in", dir, "-out", filepath.Join(t.TempDir(), "o"), "-snapshot", "0123456789abcdef0123456789abcdef")
	if r.code != cli.ExitFailure || !strings.Contains(r.stderr, "-list") {
		t.Fatalf("run = %d, stderr %q", r.code, r.stderr)
	}
}

func TestRunWithABadEnvelopeFile(t *testing.T) {
	meta, dir := openFrozen(t)
	bogus := filepath.Join(t.TempDir(), "recovery.ocbke")
	if err := os.WriteFile(bogus, []byte("not an envelope"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := runDecrypt(t, meta.RecoveryKey+"\n", "-in", dir, "-list", "-envelope", bogus)
	if r.code != cli.ExitFailure || !strings.Contains(r.stderr, "-envelope is not a recovery key envelope") {
		t.Fatalf("run = %d, stderr %q", r.code, r.stderr)
	}
}

// assertRestored compares what was restored with the frozen digests.
func assertRestored(t *testing.T, out string, want map[string]struct {
	SHA256 string `json:"sha256"`
}) {
	t.Helper()
	for name, f := range want {
		data, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != f.SHA256 {
			t.Errorf("%s: sha256 %s, want %s", name, got, f.SHA256)
		}
	}
}

// The real binary: the exit status reaches the shell, and the version the
// release stamps at link time is the one -version prints (Phase 11 stamps
// it; this proves the plumbing it will use).
func TestBinaryExitStatusAndStampedVersion(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "decrypt")
	// The package path is read from the type rather than written out, so a
	// module rename (decisions.md #26) cannot leave this test stamping a
	// variable that no longer exists.
	stampVar := reflect.TypeFor[buildinfo.Info]().PkgPath() + ".Version"
	build := exec.Command(goBin, "build", "-ldflags", "-X "+stampVar+"=v0.0.0-stamped", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	cases := []struct {
		args       []string
		code       int
		wantStdout string
	}{
		{[]string{"-version"}, cli.ExitOK, "decrypt v0.0.0-stamped ("},
		{[]string{"-h"}, cli.ExitOK, "Usage:"},
		{[]string{"-no-such-flag"}, cli.ExitUsage, ""},
		{[]string{"-in", t.TempDir(), "-verify"}, cli.ExitFailure, ""},
	}
	for _, c := range cases {
		cmd := exec.Command(bin, c.args...)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		err := cmd.Run()
		code := 0
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if code != c.code {
			t.Errorf("%v: exit status %d, want %d", c.args, code, c.code)
		}
		if !strings.Contains(stdout.String(), c.wantStdout) {
			t.Errorf("%v: stdout %q, want %q", c.args, stdout.String(), c.wantStdout)
		}
	}
}
