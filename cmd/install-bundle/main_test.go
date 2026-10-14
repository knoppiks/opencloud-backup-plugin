package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knoppiks/opencloud-backup-plugin/internal/cli"
)

func runCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestInstallsTheBundle(t *testing.T) {
	from := t.TempDir()
	if err := os.MkdirAll(filepath.Join(from, "js"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "js", "backup-vault.mjs"), []byte("export {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(t.TempDir(), "extensions", "backup-vault")

	code, stdout, stderr := runCmd(t, "-from", from, "-to", to)
	if code != cli.ExitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "into "+to+" (1 files, 9 bytes)") {
		t.Errorf("stdout = %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(to, "js", "backup-vault.mjs")); err != nil {
		t.Error(err)
	}
}

// The image runs the binary without arguments; these are the paths the
// documented initContainer and OpenCloud's web-extensions images use.
func TestDefaultsAreTheImagesPaths(t *testing.T) {
	var cfg config
	if err := flags(&cfg).Parse(nil); err != nil {
		t.Fatal(err)
	}
	if cfg.from != "/usr/share/nginx/html/backup-vault" || cfg.to != "/extensions/backup-vault" {
		t.Errorf("defaults = %q -> %q", cfg.from, cfg.to)
	}
}

func TestFailsWithoutABundle(t *testing.T) {
	to := filepath.Join(t.TempDir(), "backup-vault")
	code, _, stderr := runCmd(t, "-from", filepath.Join(t.TempDir(), "missing"), "-to", to)
	if code != cli.ExitFailure {
		t.Errorf("exit %d, want %d", code, cli.ExitFailure)
	}
	if !strings.Contains(stderr, "install-bundle: read the bundle") {
		t.Errorf("stderr = %q", stderr)
	}
	if _, err := os.Stat(to); !os.IsNotExist(err) {
		t.Errorf("destination created without a bundle: %v", err)
	}
}

func TestRefusesAPositionalArgument(t *testing.T) {
	// The shell one-liner's words, passed as arguments by an initContainer
	// that sets `args` instead of `command`, must not be ignored silently.
	code, _, stderr := runCmd(t, "sh", "-c", "cp -R /usr/share/nginx/html/backup-vault/ /extensions/")
	if code != cli.ExitUsage {
		t.Errorf("exit %d, want %d", code, cli.ExitUsage)
	}
	if !strings.Contains(stderr, `unexpected argument "sh"`) {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestVersion(t *testing.T) {
	code, stdout, _ := runCmd(t, "-version")
	if code != cli.ExitOK || !strings.HasPrefix(stdout, "install-bundle ") {
		t.Errorf("exit %d, stdout %q", code, stdout)
	}
}
