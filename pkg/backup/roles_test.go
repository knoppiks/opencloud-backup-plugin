package backup

// Which credential each job kind reaches for (decisions.md #9, Tier 2).
//
// This is the whole of the separation as far as this service is concerned: a
// backup run never holds the key that may delete, and a prune run never holds
// the key that writes the backups. Whether the storage backend turns that into
// a *bound* is a property of the backend, not of this code — see the
// CredentialSet doc comment and TestGarageGrantMatrix.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"opencloud-backup-plugin/pkg/objstore"
	"opencloud-backup-plugin/pkg/targets"
)

func TestBackupRunUsesTheBackupCredential(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	if _, err := h.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}

	got := h.engine.lastCall(t).Location.AccessKeyID
	if got != testBackupAccessKeyID {
		t.Fatalf("a backup run used access key %q, want the backup key %q",
			got, testBackupAccessKeyID)
	}
}

func TestPruneRunUsesTheMaintenanceCredential(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	if _, err := h.runner.RunPrune(ctx, testSpaceID); err != nil {
		t.Fatalf("RunPrune: %v", err)
	}

	calls := h.engine.pruneCalls()
	if len(calls) != 1 {
		t.Fatalf("engine was pruned %d times, want 1", len(calls))
	}
	got := calls[0].repo.Location.AccessKeyID
	if got != testMaintenanceAccessKeyID {
		t.Fatalf("a prune run used access key %q, want the maintenance key %q",
			got, testMaintenanceAccessKeyID)
	}
}

// The single-credential deployment: a target with one key must keep working,
// with both roles resolving to it. This is every target written before roles
// existed, and every deployment that has not separated its keys.
func TestBothRolesFallBackToASingleCredential(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	// Re-seal the target with the backup pair only.
	sealed, version, err := h.sealer.Seal(targets.CredentialSet{
		Backup: targets.PlainCreds{
			AccessKeyID:     testBackupAccessKeyID,
			SecretAccessKey: "test-secret-access-key-0000000000000000",
		},
	})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	target, err := h.targets.GetTarget(ctx, testTargetID)
	if err != nil {
		t.Fatalf("GetTarget: %v", err)
	}
	target.WrappedCreds, target.Version = sealed, version
	if _, err := h.targets.UpdateTarget(ctx, target); err != nil {
		t.Fatalf("UpdateTarget: %v", err)
	}

	if _, err := h.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}
	if got := h.engine.lastCall(t).Location.AccessKeyID; got != testBackupAccessKeyID {
		t.Fatalf("backup used %q, want %q", got, testBackupAccessKeyID)
	}

	if _, err := h.runner.RunPrune(ctx, testSpaceID); err != nil {
		t.Fatalf("RunPrune: %v", err)
	}
	calls := h.engine.pruneCalls()
	if len(calls) != 1 {
		t.Fatalf("engine was pruned %d times, want 1", len(calls))
	}
	if got := calls[0].repo.Location.AccessKeyID; got != testBackupAccessKeyID {
		t.Fatalf("prune used %q, want the target's only key %q", got, testBackupAccessKeyID)
	}
}

// fakeProber records what it was asked and answers what a test told it to.
type fakeProber struct {
	mu     sync.Mutex
	calls  []objstore.S3Config
	caps   objstore.Capabilities
	err    error
	onCall func()
}

func (p *fakeProber) Probe(_ context.Context, cfg objstore.S3Config) (objstore.Capabilities, error) {
	p.mu.Lock()
	p.calls = append(p.calls, cfg)
	onCall := p.onCall
	caps, err := p.caps, p.err
	p.mu.Unlock()

	if onCall != nil {
		onCall()
	}
	return caps, err
}

func (p *fakeProber) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func TestPruneObservesTargetImmutability(t *testing.T) {
	ctx := context.Background()
	prober := &fakeProber{caps: objstore.Capabilities{ObjectLock: objstore.ObjectLockUnsupported}}
	h := newHarness(t, func(d *Deps) { d.Immutability = prober })

	if _, err := h.runner.RunPrune(ctx, testSpaceID); err != nil {
		t.Fatalf("RunPrune: %v", err)
	}
	if prober.callCount() != 1 {
		t.Fatalf("target was probed %d times, want 1", prober.callCount())
	}
	// It must ask about the bucket the prune is about to act on, with the
	// credential that run resolved.
	if got := prober.calls[0].AccessKeyID; got != testMaintenanceAccessKeyID {
		t.Fatalf("probe used access key %q, want the maintenance key", got)
	}
	if !strings.Contains(h.logs.String(), "target immutability observed") {
		t.Fatal("the observation was not reported")
	}
}

// A backup is not the run that would use object lock, and probing on every run
// would be a per-run cost for a daily question.
func TestBackupDoesNotProbe(t *testing.T) {
	ctx := context.Background()
	prober := &fakeProber{}
	h := newHarness(t, func(d *Deps) { d.Immutability = prober })

	if _, err := h.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}
	if prober.callCount() != 0 {
		t.Fatalf("a backup run probed the target %d times", prober.callCount())
	}
}

// The observation is a diagnostic. A target that will not answer it must not
// cost a household its retention.
func TestPruneSucceedsWhenTheProbeFails(t *testing.T) {
	ctx := context.Background()
	prober := &fakeProber{err: errors.New("endpoint unreachable")}
	h := newHarness(t, func(d *Deps) { d.Immutability = prober })

	if _, err := h.runner.RunPrune(ctx, testSpaceID); err != nil {
		t.Fatalf("a failed probe must not fail the prune: %v", err)
	}
	if len(h.engine.pruneCalls()) != 1 {
		t.Fatal("the prune did not run")
	}
}

// Nothing observed, nothing claimed.
func TestPruneRunsWithoutAProber(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, func(d *Deps) { d.Immutability = nil })

	if _, err := h.runner.RunPrune(ctx, testSpaceID); err != nil {
		t.Fatalf("RunPrune: %v", err)
	}
	if strings.Contains(h.logs.String(), "target immutability observed") {
		t.Fatal("an unconfigured probe reported an observation")
	}
}
