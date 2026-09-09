package backup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/spacecfg"
)

func TestRunPrune_AppliesTheSpacesWindowAndRecordsWhatItDid(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.engine.pruneStats = snapshot.PruneStats{Deleted: 3, Kept: 1}

	res, err := h.runner.RunPrune(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("RunPrune: %v", err)
	}
	if res.Deleted != 3 || res.Kept != 1 || res.SpaceID != testSpaceID {
		t.Fatalf("result = %+v", res)
	}

	calls := h.engine.pruneCalls()
	if len(calls) != 1 {
		t.Fatalf("engine was pruned %d times, want 1", len(calls))
	}
	if calls[0].window != spacecfg.DefaultRetentionWindow {
		t.Fatalf("window = %s, want the space's effective window %s",
			calls[0].window, spacecfg.DefaultRetentionWindow)
	}
	if calls[0].repo.Space.SpaceID != testSpaceID {
		t.Fatalf("pruned the wrong space: %+v", calls[0].repo)
	}

	job, err := h.jobs.Get(ctx, res.JobID)
	if err != nil {
		t.Fatalf("Get job: %v", err)
	}
	if job.Kind != jobs.KindPrune {
		t.Fatalf("job kind = %q, want prune", job.Kind)
	}
	if job.State != jobs.StateSucceeded {
		t.Fatalf("job state = %q", job.State)
	}
	if job.SnapshotsDeleted != 3 || job.SnapshotsKept != 1 {
		t.Fatalf("job counts = %+v", job)
	}
}

// A window stored before the floor existed, or missing entirely, must not make
// a prune delete history the API would have refused to give up.
func TestRunPrune_RaisesAWindowBelowTheFloor(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	cfg, err := h.configs.Get(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("Get config: %v", err)
	}
	cfg.RetentionWindow = time.Hour
	if _, err := h.configs.Put(ctx, cfg); err != nil {
		t.Fatalf("Put config: %v", err)
	}

	if _, err := h.runner.RunPrune(ctx, testSpaceID); err != nil {
		t.Fatalf("RunPrune: %v", err)
	}
	calls := h.engine.pruneCalls()
	if len(calls) != 1 || calls[0].window != spacecfg.MinRetentionWindow {
		t.Fatalf("window = %v, want the floor %s", calls, spacecfg.MinRetentionWindow)
	}
}

// The Data Key exists for the prune and not a moment longer, exactly as for a
// backup: it is the same key, unwrapped by the same path.
func TestRunPrune_ZeroizesTheDataKey(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	var live []byte
	h.engine.onPrune = func(r snapshot.Repo) { live = r.DK }

	if _, err := h.runner.RunPrune(ctx, testSpaceID); err != nil {
		t.Fatalf("RunPrune: %v", err)
	}
	if live == nil {
		t.Fatal("prune was handed no data key")
	}
	for _, b := range live {
		if b != 0 {
			t.Fatal("the data key buffer was not zeroized after the prune")
		}
	}
	if strings.Contains(h.logs.String(), string(h.dk)) {
		t.Fatal("the logs contain key material")
	}
}

// Two runs of a Space must never overlap, whatever kind they are: a prune
// deletes manifests and rewrites indexes in the repository a backup is writing.
func TestRunPrune_RefusedWhileAnotherRunHoldsTheLock(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	release, err := h.jobs.Acquire(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()

	if _, err := h.runner.RunPrune(ctx, testSpaceID); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("RunPrune = %v, want ErrRunInProgress", err)
	}
	if calls := h.engine.pruneCalls(); len(calls) != 0 {
		t.Fatalf("the engine was pruned anyway: %+v", calls)
	}
}

// And the converse: a prune holds the lock while it runs, so a backup started
// underneath it is refused rather than queued behind it.
func TestRunPrune_HoldsTheRunLockWhileItRuns(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	var backupErr error
	h.engine.onPrune = func(snapshot.Repo) {
		_, backupErr = h.runner.RunBackup(ctx, testSpaceID)
	}

	if _, err := h.runner.RunPrune(ctx, testSpaceID); err != nil {
		t.Fatalf("RunPrune: %v", err)
	}
	if !errors.Is(backupErr, ErrRunInProgress) {
		t.Fatalf("backup during prune = %v, want ErrRunInProgress", backupErr)
	}
	if h.engine.callCount() != 0 {
		t.Fatal("a backup ran while the prune held the lock")
	}
}

func TestRunPrune_RecordsASanitizedFailure(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.engine.pruneErr = errors.New("bucket 'family-backups' returned 403 for key spaces/x/kopia.repository")

	_, err := h.runner.RunPrune(ctx, testSpaceID)
	if !errors.Is(err, ErrPruneFailed) {
		t.Fatalf("RunPrune = %v, want ErrPruneFailed", err)
	}

	history, err := h.jobs.ListRecentOfKind(ctx, testSpaceID, jobs.KindPrune, 1)
	if err != nil {
		t.Fatalf("ListRecentOfKind: %v", err)
	}
	if len(history) != 1 || history[0].State != jobs.StateFailed {
		t.Fatalf("history = %+v", history)
	}
	// A failed prune must not read as a failed backup: the data is exactly
	// where it was, and only the cleanup did not happen.
	if history[0].Error != "cleaning up expired backups failed" {
		t.Fatalf("job error = %q", history[0].Error)
	}
	if strings.Contains(history[0].Error, "family-backups") {
		t.Fatalf("the job record leaks target detail: %q", history[0].Error)
	}
}

// A Space that is not configured for backup has no repository to prune, and
// says so with the same error a backup would.
func TestRunPrune_RefusesAnUnconfiguredSpace(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	if err := h.configs.Delete(ctx, testSpaceID); err != nil {
		t.Fatalf("Delete config: %v", err)
	}
	if _, err := h.runner.RunPrune(ctx, testSpaceID); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("RunPrune = %v, want ErrNotConfigured", err)
	}
}

// The lock has to come back, or the Space is wedged until the lease expires.
func TestRunPrune_ReleasesTheLockOnBothOutcomes(t *testing.T) {
	ctx := context.Background()

	for name, fail := range map[string]bool{"success": false, "failure": true} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			if fail {
				h.engine.pruneErr = errors.New("nope")
			}

			_, err := h.runner.RunPrune(ctx, testSpaceID)
			if fail != (err != nil) {
				t.Fatalf("RunPrune = %v", err)
			}

			release, err := h.jobs.Acquire(ctx, testSpaceID)
			if err != nil {
				t.Fatalf("the run lock was not released: %v", err)
			}
			release()
		})
	}
}
