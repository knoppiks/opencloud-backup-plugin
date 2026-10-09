package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/instance"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/state"
)

// minimalServiceEnv is the smallest configuration buildService accepts: no
// OIDC, no CS3, no keys, state supplied by the test.
func minimalServiceEnv(t *testing.T) []string {
	t.Helper()
	return []string{"BACKUP_WORK_DIR_ALLOW_DISK=true", "BACKUP_WORK_DIR=" + t.TempDir()}
}

// sharedState hands every startup the same store, as a restarted pod finds
// the same state Space.
func sharedState(st state.Store) startupDeps {
	return startupDeps{openState: func(stateEnv, *cs3.Client, string, *slog.Logger) (state.Store, error) {
		return st, nil
	}}
}

// A startup that fails after registering the instance must withdraw the
// registration. Otherwise every restart inside its TTL fails with "another
// instance is running" and the real error is never seen again
// (review-2026-10.md F1).
func TestBuildService_AFailedStartupReleasesTheInstanceRecord(t *testing.T) {
	env := minimalServiceEnv(t)
	st := state.NewMemoryStore()

	// Fails in targets.Bootstrap, well after the claim: seeding is switched
	// on with no target to seed. Loading refuses that configuration, so it is
	// switched on behind the loader's back: what is under test is what
	// buildService does when something after the claim fails.
	broken := testConfig(t, append(env, "TW_KEY="+encodedKey(t))...)
	broken.Bootstrap.Enable = true
	// The returned cleanup is deliberately not called: buildService owns
	// releasing what it acquired when it fails.
	_, _, err := buildService(context.Background(), broken, discardLogger(), sharedState(st))
	if err == nil || !strings.Contains(err.Error(), "bootstrap") {
		t.Fatalf("first start = %v, want the bootstrap error", err)
	}

	// The restart, inside the record's TTL, gets past the guard.
	cfg := testConfig(t, env...)
	svc, cleanup, err := buildService(context.Background(), cfg, discardLogger(), sharedState(st))
	if errors.Is(err, instance.ErrAnotherInstance) {
		t.Fatalf("restart refused by the failed start's instance record: %v", err)
	}
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if svc.api == nil || svc.background == nil {
		t.Fatalf("service = %+v, want it wired", svc)
	}
	cleanup()

	// And a clean stop releases it as well.
	_, cleanup, err = buildService(context.Background(), cfg, discardLogger(), sharedState(st))
	if err != nil {
		t.Fatalf("start after a clean stop: %v", err)
	}
	cleanup()
}

// The live instance is still protected: the guard has not been weakened.
func TestBuildService_ASecondLiveInstanceIsStillRefused(t *testing.T) {
	cfg := testConfig(t, minimalServiceEnv(t)...)
	st := state.NewMemoryStore()

	_, cleanup, err := buildService(context.Background(), cfg, discardLogger(), sharedState(st))
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	defer cleanup()

	_, cleanup2, err := buildService(context.Background(), cfg, discardLogger(), sharedState(st))
	defer cleanup2()
	if !errors.Is(err, instance.ErrAnotherInstance) {
		t.Fatalf("second start = %v, want ErrAnotherInstance", err)
	}
}

// fakeProvisioner stands in for the CS3 gateway.
type fakeProvisioner struct{ id string }

func (f fakeProvisioner) Create(context.Context, string) (string, error) { return f.id, nil }
func (f fakeProvisioner) Check(context.Context, string) error            { return nil }

// `STATE_SPACE_ID=$(backupd provision-state-space)` must capture the id and
// nothing else: the log line goes to stderr (review-2026-10.md F6).
func TestProvisionStateSpace_StdoutIsExactlyTheID(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := provisionStateSpace(context.Background(), "state", fakeProvisioner{id: "space-123"},
		newLogger(&stderr), &stdout)
	if err != nil {
		t.Fatalf("provisionStateSpace: %v", err)
	}
	if got := stdout.String(); got != "space-123\n" {
		t.Fatalf("stdout = %q, want exactly the id", got)
	}
	if !strings.Contains(stderr.String(), "state space created") {
		t.Fatalf("the log line did not go to stderr: %q", stderr.String())
	}
}

func TestRun_ACommandFailureLogsToStderrOnly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"no-such-command"}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing", stdout.String())
	}
	if !strings.Contains(stderr.String(), "command failed") {
		t.Fatalf("stderr = %q, want the failure", stderr.String())
	}
}

// The drain must cover a run's outcome write in the worst case, or a run cut
// short by shutdown stays "running" until its lease expires; and the whole
// shutdown must fit in the grace period the shipped manifest gives the pod
// (review-2026-10.md F7).
func TestShutdownBudgetCoversTheOutcomeWriteAndFitsTheGracePeriod(t *testing.T) {
	if drainTimeout < jobs.OutcomeWorstCase {
		t.Fatalf("drain %v is shorter than the outcome write's worst case %v", drainTimeout, jobs.OutcomeWorstCase)
	}

	manifest, err := os.ReadFile("../../deploy/deployment-backupd.yaml")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	m := regexp.MustCompile(`terminationGracePeriodSeconds:\s*(\d+)`).FindSubmatch(manifest)
	if m == nil {
		t.Fatal("the manifest sets no terminationGracePeriodSeconds")
	}
	seconds, _ := strconv.Atoi(string(m[1]))
	grace := time.Duration(seconds) * time.Second
	if total := httpShutdownTimeout + drainTimeout; total >= grace {
		t.Fatalf("shutdown can take %v, the pod gets %v", total, grace)
	}
}
