package main

// The whole program against a "bucket" on disk. The bucket is laid out from
// the frozen Take-Out (pkg/takeout/testdata): its repository where the S3
// target keeps a Space's repository, its envelope where every backup run
// publishes it. So a take-out of it must reproduce a Take-Out that verifies.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"opencloud-backup-plugin/internal/buildinfo"
	"opencloud-backup-plugin/internal/cli"
	"opencloud-backup-plugin/pkg/objstore"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/snapshot/s3repo"
	"opencloud-backup-plugin/pkg/takeout"
)

const (
	frozenTakeOut = "../../pkg/takeout/testdata/takeout-v1"
	frozenPrefix  = "oc/"
	frozenSpace   = "storage-1$space-1"
)

// credentials is an environment with both S3 variables set. The values reach
// only the fake target.
func credentials() []string {
	return []string{"S3_ACCESS_KEY_ID=test-access-id", "S3_SECRET_ACCESS_KEY=" + strings.Join([]string{"not", "a", "secret"}, "-")}
}

// newBucket lays the frozen Take-Out out as the target holds it.
func newBucket(t *testing.T, withEnvelope bool) string {
	t.Helper()
	bucket := t.TempDir()
	ref := snapshot.SpaceRef{SpaceID: frozenSpace}
	loc := snapshot.Location{Prefix: frozenPrefix}

	repo := filepath.Join(bucket, filepath.FromSlash(snapshot.RepoPrefix(loc, ref)))
	if err := os.CopyFS(repo, os.DirFS(filepath.Join(frozenTakeOut, "repo"))); err != nil {
		t.Fatalf("copy repository: %v", err)
	}
	if withEnvelope {
		blob, err := os.ReadFile(filepath.Join(frozenTakeOut, "recovery.ocbke"))
		if err != nil {
			t.Fatal(err)
		}
		if err := (objstore.DirStore{Root: bucket}).Put(context.Background(), snapshot.EnvelopeKey(loc, ref), blob); err != nil {
			t.Fatal(err)
		}
	}
	return bucket
}

// dirTarget opens bucket instead of S3, and records the configuration it was
// opened with.
func dirTarget(bucket string, seen *config) openTarget {
	return func(_ context.Context, cfg config) (snapshot.StorageOpener, objstore.Store, error) {
		if seen != nil {
			*seen = cfg
		}
		return snapshot.FilesystemOpener{Root: bucket}, objstore.DirStore{Root: bucket}, nil
	}
}

// unreachable is a target a test must never get to.
func unreachable(context.Context, config) (snapshot.StorageOpener, objstore.Store, error) {
	return nil, nil, errors.New("the target must not be opened")
}

type result struct {
	code           int
	stdout, stderr string
}

func runTakeout(t *testing.T, environ []string, open openTarget, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, environ, &stdout, &stderr, open)
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func takeOutArgs(out string, extra ...string) []string {
	return append([]string{"-bucket", "backups", "-prefix", frozenPrefix, "-space", frozenSpace, "-out", out}, extra...)
}

func TestRunWritesAVerifiedTakeOut(t *testing.T) {
	var seen config
	out := filepath.Join(t.TempDir(), "takeout")
	r := runTakeout(t, credentials(), dirTarget(newBucket(t, true), &seen), takeOutArgs(out, "-plain-http", "-v")...)
	if r.code != cli.ExitOK {
		t.Fatalf("run = %d, stderr %q", r.code, r.stderr)
	}
	for _, want := range []string{"take-out written to " + out, "space:     " + frozenSpace, "envelope:  version 1", "decrypt -in " + out} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout does not say %q:\n%s", want, r.stdout)
		}
	}
	// -v logs progress, to stderr only.
	if !strings.Contains(r.stderr, "level=INFO") {
		t.Errorf("-v logged nothing: %q", r.stderr)
	}
	if strings.Contains(r.stderr, "not-a-secret") || strings.Contains(r.stdout, "not-a-secret") {
		t.Fatal("the secret access key was printed")
	}
	if !seen.plainHTTP || seen.accessKey != "test-access-id" {
		t.Fatalf("target opened with %+v", seen)
	}
	if err := takeout.Verify(context.Background(), out); err != nil {
		t.Fatalf("the written take-out does not verify: %v", err)
	}
}

// The old spelling keeps working for one minor release, with a warning that
// names the new one (compatibility-policy.md §2).
func TestInsecureIsADeprecatedSpellingOfPlainHTTP(t *testing.T) {
	var seen config
	r := runTakeout(t, credentials(), dirTarget(newBucket(t, true), &seen),
		takeOutArgs(filepath.Join(t.TempDir(), "takeout"), "-insecure")...)
	if r.code != cli.ExitOK {
		t.Fatalf("run = %d, stderr %q", r.code, r.stderr)
	}
	if !seen.plainHTTP {
		t.Fatal("-insecure no longer selects plain HTTP")
	}
	if !strings.Contains(r.stderr, "-insecure is deprecated") || !strings.Contains(r.stderr, "use -plain-http") {
		t.Fatalf("stderr = %q, want the deprecation warning", r.stderr)
	}

	// Without the old name there is no warning.
	r = runTakeout(t, credentials(), dirTarget(newBucket(t, true), nil),
		takeOutArgs(filepath.Join(t.TempDir(), "takeout"), "-plain-http")...)
	if strings.Contains(r.stderr, "deprecated") {
		t.Fatalf("stderr = %q", r.stderr)
	}
}

func TestDeprecatedAliasParsesLikeABool(t *testing.T) {
	var target, used bool
	alias := deprecatedAlias{target: &target, used: &used}
	if err := alias.Set("nope"); err == nil {
		t.Fatal("a non-boolean was accepted")
	}
	if err := alias.Set("true"); err != nil || !target || !used || alias.String() != "true" {
		t.Fatalf("Set(true): err %v, target %v, used %v", err, target, used)
	}
	if !alias.IsBoolFlag() || (deprecatedAlias{}).String() != "false" {
		t.Fatal("the alias must behave as a boolean flag")
	}
}

func TestRunRefusesAMissingEnvelopeUnlessAllowed(t *testing.T) {
	bucket := newBucket(t, false)

	r := runTakeout(t, credentials(), dirTarget(bucket, nil), takeOutArgs(filepath.Join(t.TempDir(), "a"))...)
	if r.code != cli.ExitFailure || !strings.Contains(r.stderr, "-allow-missing-envelope") {
		t.Fatalf("run = %d, stderr %q", r.code, r.stderr)
	}

	r = runTakeout(t, credentials(), dirTarget(bucket, nil),
		takeOutArgs(filepath.Join(t.TempDir(), "b"), "-allow-missing-envelope")...)
	if r.code != cli.ExitOK || !strings.Contains(r.stdout, "envelope:  MISSING") {
		t.Fatalf("run = %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
}

func TestRunReportsATargetThatCannotBeOpened(t *testing.T) {
	r := runTakeout(t, credentials(), unreachable, takeOutArgs(t.TempDir())...)
	if r.code != cli.ExitFailure || !strings.Contains(r.stderr, "takeout: the target must not be opened") {
		t.Fatalf("run = %d, stderr %q", r.code, r.stderr)
	}
}

func TestRunUsage(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{"help", []string{"-h"}, cli.ExitOK, "cannot decrypt anything", ""},
		{"help names the new flag", []string{"-help"}, cli.ExitOK, "-plain-http", ""},
		{"help explains exit status", []string{"-h"}, cli.ExitOK, "Exit status", ""},
		{"version", []string{"-version"}, cli.ExitOK, "takeout " + buildinfo.DevVersion + " (", ""},
		{"unknown flag", []string{"-key", "x"}, cli.ExitUsage, "", "flag provided but not defined: -key"},
		{"stray argument", takeOutArgs("o", "extra"), cli.ExitUsage, "", `unexpected argument "extra"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runTakeout(t, nil, unreachable, c.args...)
			if r.code != c.code {
				t.Fatalf("run = %d, want %d (stderr %q)", r.code, c.code, r.stderr)
			}
			if !strings.Contains(r.stdout, c.stdout) || !strings.Contains(r.stderr, c.stderr) {
				t.Fatalf("stdout %q, stderr %q", r.stdout, r.stderr)
			}
		})
	}
}

// The production target is built without touching the network: nothing is
// sent before the copy starts.
func TestS3TargetIsBuiltOffline(t *testing.T) {
	repos, objects, err := s3Target(context.Background(), config{
		endpoint: "127.0.0.1:1", region: "garage", bucket: "b",
		accessKey: "test-access-id", secretKey: "not-a-secret", plainHTTP: true,
	})
	if err != nil {
		t.Fatalf("s3Target: %v", err)
	}
	if _, ok := repos.(s3repo.Opener); !ok || objects == nil {
		t.Fatalf("s3Target = %T, %T", repos, objects)
	}

	if _, _, err := s3Target(context.Background(), config{bucket: "b"}); err == nil {
		t.Fatal("a target without credentials was built")
	}
}

func TestExplainCorruption(t *testing.T) {
	if got := explain(takeout.ErrCorrupt); !errors.Is(got, takeout.ErrCorrupt) || !strings.Contains(got.Error(), "re-run") {
		t.Fatalf("explain = %v", got)
	}
}
