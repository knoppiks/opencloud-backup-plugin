package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"opencloud-backup-plugin/internal/buildinfo"
	"opencloud-backup-plugin/internal/cli"
	"opencloud-backup-plugin/internal/config"
)

// runCommand runs an operator command against a configuration the test
// built, the way runCommandLine does after loading one.
func runCommand(t *testing.T, cfg config.Backupd, name string, args ...string) error {
	t.Helper()
	cmd, ok := lookupCommand(name)
	if !ok {
		return unknownCommand(name)
	}
	act, err := cmd.parse(args, io.Discard, io.Discard)
	if err != nil {
		return err
	}
	return act(context.Background(), cfg, discardLogger(), io.Discard)
}

type result struct {
	code           int
	stdout, stderr string
}

func runBackupd(environ []string, args ...string) result {
	var stdout, stderr bytes.Buffer
	code := run(args, environ, &stdout, &stderr)
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// A configuration the service would refuse. Help, version and wrong usage
// must not need a valid one: they are what an operator reaches for while
// the configuration is still being written.
var brokenEnviron = []string{"OIDC_ISSUER=https://issuer.invalid"}

func TestVersion(t *testing.T) {
	for _, arg := range []string{"version", "-version", "--version"} {
		r := runBackupd(brokenEnviron, arg)
		if r.code != cli.ExitOK || !strings.HasPrefix(r.stdout, "backupd "+buildinfo.DevVersion+" (") {
			t.Errorf("%s: run = %d, stdout %q, stderr %q", arg, r.code, r.stdout, r.stderr)
		}
	}
	if r := runBackupd(nil, "version", "extra"); r.code != cli.ExitUsage {
		t.Errorf("version with an argument: run = %d", r.code)
	}
}

func TestHelpListsTheCommands(t *testing.T) {
	for _, arg := range []string{"help", "-h", "-help", "--help"} {
		r := runBackupd(brokenEnviron, arg)
		if r.code != cli.ExitOK {
			t.Fatalf("%s: run = %d, stderr %q", arg, r.code, r.stderr)
		}
		for _, c := range commands() {
			if !strings.Contains(r.stdout, c.name) || !strings.Contains(r.stdout, c.summary) {
				t.Errorf("%s: help does not list %s:\n%s", arg, c.name, r.stdout)
			}
		}
		for _, want := range []string{"version", "Exit status"} {
			if !strings.Contains(r.stdout, want) {
				t.Errorf("%s: help does not mention %q", arg, want)
			}
		}
	}
}

func TestHelpForOneCommand(t *testing.T) {
	cases := map[string]string{
		"provision-state-space": "usage: backupd provision-state-space",
		"rotate-srw":            "SRW_KEY_OLD",
		"rotate-tw":             "TW_KEY_OLD",
	}
	for name, want := range cases {
		for _, args := range [][]string{{"help", name}, {name, "-h"}} {
			r := runBackupd(brokenEnviron, args...)
			if r.code != cli.ExitOK || !strings.Contains(r.stdout, want) {
				t.Errorf("%v: run = %d, stdout %q, stderr %q", args, r.code, r.stdout, r.stderr)
			}
		}
	}
}

func TestWrongUsageExitsTwo(t *testing.T) {
	cases := map[string]struct {
		args []string
		want string
	}{
		"unknown command":          {[]string{"no-such-command"}, `unknown command "no-such-command"`},
		"unknown flag first":       {[]string{"-config", "x"}, `unknown command "-config"`},
		"help for unknown command": {[]string{"help", "nope"}, `unknown command "nope"`},
		"help with two commands":   {[]string{"help", "rotate-srw", "rotate-tw"}, "at most one"},
		"unknown command flag":     {[]string{"rotate-srw", "-force"}, "flag provided but not defined: -force"},
		"stray argument":           {[]string{"provision-state-space", "extra"}, `unexpected argument "extra"`},
		"rotation not asserted":    {[]string{"rotate-tw"}, "-service-stopped"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := runBackupd(brokenEnviron, c.args...)
			if r.code != cli.ExitUsage {
				t.Fatalf("run = %d, want %d (stderr %q)", r.code, cli.ExitUsage, r.stderr)
			}
			if !strings.Contains(r.stderr, c.want) {
				t.Fatalf("stderr = %q, want %q", r.stderr, c.want)
			}
			if r.stdout != "" {
				t.Fatalf("stdout = %q", r.stdout)
			}
			// The configuration was never read: its error is not reported.
			if strings.Contains(r.stderr, "OIDC") {
				t.Fatalf("a usage error read the configuration: %q", r.stderr)
			}
		})
	}
}

// The usage error names where to look next.
func TestUsageErrorPointsToHelp(t *testing.T) {
	r := runBackupd(nil, "rotate-tw")
	if !strings.Contains(r.stderr, "Run 'backupd rotate-tw -h' for usage.") {
		t.Fatalf("stderr = %q", r.stderr)
	}
}

func TestEveryCommandIsListedOnce(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range commands() {
		if seen[c.name] || c.summary == "" || c.parse == nil {
			t.Errorf("command %q is duplicated or incomplete", c.name)
		}
		seen[c.name] = true
		if isHelp(c.name) || isVersion(c.name) {
			t.Errorf("command %q shadows help or version", c.name)
		}
	}
}
