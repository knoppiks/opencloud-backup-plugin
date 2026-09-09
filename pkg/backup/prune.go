package backup

// Prune runs: the other half of retention.
//
// A backup run writes; nothing it does ever gives storage back. Expiring
// snapshots past the Space's keep-within window, and reclaiming what they and
// any interrupted run were holding, happens here — as its own job kind, under
// the same per-Space run lock, never inline in a backup (decisions.md #9
// Tier 1).
//
// It shares this package with the backup runner because it needs exactly the
// same three secrets in the same order — the target's credentials, the Space's
// Data Key, and nothing else — and duplicating that resolution somewhere else
// would mean two places where a plaintext Data Key can be mishandled.

import (
	"context"
	"fmt"
	"time"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/snapshot"
)

// PruneResult summarises a completed prune run. Like Result it carries no key
// material — only counts and identifiers the status board may show.
type PruneResult struct {
	JobID   string
	SpaceID string
	// Deleted is how many snapshots the run expired, Kept how many restorable
	// snapshots the Space still has.
	Deleted    int
	Kept       int
	StartedAt  time.Time
	FinishedAt time.Time
}

// RunPrune applies a Space's retention window and reclaims what expires.
//
// It is unattended work, so it is bounded in time exactly like a scheduled
// backup: maintenance against a target that accepts a connection and then stops
// responding must not hold a Space's run lock until the process is restarted.
//
// The run is refused while a backup or a restore of the same Space is under way
// (ErrRunInProgress). That is the whole reason it takes the run lock rather
// than trusting the scheduler not to overlap it: deleting manifests and
// rewriting indexes underneath a running backup is how a repository gets
// corrupted.
func (r *Runner) RunPrune(ctx context.Context, spaceID string) (PruneResult, error) {
	runCtx, cancel := context.WithTimeout(ctx, r.runTimeout())
	defer cancel()

	pending, err := r.begin(runCtx, spaceID, jobs.KindPrune, jobs.TriggerSchedule)
	if err != nil {
		return PruneResult{}, err
	}
	return r.finishPrune(runCtx, pending)
}

// finishPrune performs the prune, records the outcome, and gives the run lock
// back. It owns the lock from the moment begin returned it.
func (r *Runner) finishPrune(ctx context.Context, pending run) (PruneResult, error) {
	stats, err := r.pruneSpace(ctx, pending.space)
	if err != nil {
		r.fail(ctx, pending, err)
		return PruneResult{}, err
	}

	r.settle(ctx, pending, jobs.Outcome{
		State:            jobs.StateSucceeded,
		SnapshotsDeleted: stats.Deleted,
		SnapshotsKept:    stats.Kept,
	})

	finishedAt := r.deps.Clock.Now()
	r.deps.Logger.Info("prune run succeeded",
		"space", pending.space.ID,
		"job", pending.job.ID,
		"deleted", stats.Deleted,
		"kept", stats.Kept,
		"duration", finishedAt.Sub(pending.startedAt),
	)

	return PruneResult{
		JobID:      pending.job.ID,
		SpaceID:    pending.space.ID,
		Deleted:    stats.Deleted,
		Kept:       stats.Kept,
		StartedAt:  pending.startedAt,
		FinishedAt: finishedAt,
	}, nil
}

// pruneSpace resolves the run's secrets, prunes, and disposes of the secrets.
// Keeping it in one function bounds the lifetime of both the Data Key and the
// target credentials to a single call frame, exactly as snapshotSpace does.
func (r *Runner) pruneSpace(ctx context.Context, space cs3.Space) (snapshot.PruneStats, error) {
	repo, cfg, _, err := r.openRepo(ctx, space.ID)
	if err != nil {
		return snapshot.PruneStats{}, err
	}
	defer keys.Zeroize(repo.DK)

	// EffectiveRetentionWindow, not the raw field: a window stored before the
	// floor existed — or absent altogether — must not make this delete history
	// the API would have refused to give up (decisions.md R5 amendment).
	stats, err := r.deps.Engine.Prune(ctx, repo, cfg.EffectiveRetentionWindow())
	if err != nil {
		// The detail carries no secrets but does describe the target; log it
		// and return something a job record can hold.
		r.deps.Logger.Error("prune failed", "space", space.ID, "err", err)
		return snapshot.PruneStats{}, fmt.Errorf("%w: %w", ErrPruneFailed, err)
	}
	return stats, nil
}
