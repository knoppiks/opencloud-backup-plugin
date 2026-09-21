package backup

// Prune runs: the other half of retention.
//
// A backup run writes; nothing it does ever gives storage back. Expiring
// snapshots past the Space's keep-within window, and reclaiming what they and
// any interrupted run were holding, happens here — as its own job kind, under
// the same per-Space run lock, never inline in a backup (decisions.md #9
// Tier 1).
//
// It shares this package with the backup runner because it needs the same two
// secrets resolved the same way — the target's credentials and the Space's Data
// Key — and duplicating that resolution somewhere else would mean two places
// where a plaintext Data Key can be mishandled. It does not resolve the *same*
// credentials: a prune asks for the target's maintenance role, so this is the
// only file in the service that ever holds that key (decisions.md #9, Tier 2).
// What that key may *do* is the backend's business, and on Garage it is no more
// than the backup key may do.

import (
	"context"
	"fmt"
	"time"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/objstore"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/targets"
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
	repo, cfg, target, err := r.openRepo(ctx, space.ID, targets.RoleMaintenance)
	if err != nil {
		return snapshot.PruneStats{}, err
	}
	defer keys.Zeroize(repo.DK)

	r.observeImmutability(ctx, space.ID, target)

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

// observeImmutability asks the target whether it can make objects undeletable,
// and says so when the answer changes what this deployment could do.
//
// It runs here, on the prune, for two reasons: this is the run that would use
// Object Lock if there were anything to use, and the prune's slow cadence means
// the question is re-asked forever rather than answered once at a deployment's
// first start and never again. A backend that grows the feature therefore shows
// up in the log by itself.
//
// Supported is logged at info because an operator can act on it. Anything else
// is logged at debug: the expected answer is "unsupported", and a daily line
// per Space saying the expected thing is noise that teaches people to ignore
// the log. Nothing here can fail a prune — the observation is a diagnostic, not
// a precondition.
func (r *Runner) observeImmutability(ctx context.Context, spaceID string, target resolvedTarget) {
	if r.deps.Immutability == nil {
		return
	}

	caps, err := r.deps.Immutability.Probe(ctx, target.s3)
	if err != nil {
		r.deps.Logger.Debug("could not probe target immutability", "space", spaceID, "err", err)
		return
	}

	level := r.deps.Logger.Debug
	if caps.ObjectLock == objstore.ObjectLockSupported {
		level = r.deps.Logger.Info
	}
	level("target immutability observed",
		"space", spaceID,
		"object_lock", caps.ObjectLock.String(),
		"object_lock_enabled", caps.ObjectLockEnabled,
		"versioning_enabled", caps.VersioningEnabled,
	)
}
