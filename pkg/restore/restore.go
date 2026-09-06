// Package restore implements restore Path B: a user restoring their own Space
// from a snapshot, back into OpenCloud (decisions.md, "Restore paths").
//
//	[S3 target] -> [kopia read/decrypt] -> [CS3 upload]
//
// It is the mirror image of pkg/backup, and shares its rules:
//
//   - Authorization is the caller's, not the worker's: the API only ever hands
//     this package a Space the caller is a *member* of. There is no admin path
//     into a user's Space (decisions.md #2, #15) — being an admin grants nothing
//     here.
//   - v1 restores a whole snapshot into a fresh "Restore/<timestamp>/" folder.
//     Live data is never overwritten and never deleted (decisions.md #3).
//   - Nothing is staged on disk: entries stream from kopia straight into CS3,
//     the same stance the backup path takes.
//   - The plaintext Data Key and the target's credentials exist only in memory
//     for the duration of a run.
package restore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"time"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/targets"
)

// RestoreFolder is the top-level folder every restore lands in. A restore never
// touches live data, so everything is written below RestoreFolder/<timestamp>/.
const RestoreFolder = "Restore"

// DefaultRunTimeout bounds a background restore. Like a backup run it is
// generous: a full Space may be large and being killed halfway is worse than
// being slow.
const DefaultRunTimeout = 6 * time.Hour

// Sentinel errors the API maps onto status codes. Their messages are user-safe:
// they never name internal services, endpoints, or key material.
var (
	// ErrSpaceNotFound means the worker credential cannot see the Space.
	ErrSpaceNotFound = errors.New("restore: space not found")
	// ErrNotConfigured means the Space has no backup configuration or no keys.
	ErrNotConfigured = errors.New("restore: backup is not configured for this space")
	// ErrTargetUnavailable means the target is missing or unreadable.
	ErrTargetUnavailable = errors.New("restore: backup target is unavailable")
	// ErrSnapshotNotFound means the requested snapshot is not in the repository.
	ErrSnapshotNotFound = errors.New("restore: no such snapshot")
	// ErrRunInProgress means another run holds the Space's lock.
	ErrRunInProgress = errors.New("restore: a run is already in progress for this space")
	// ErrRunFailed means the restore itself failed.
	ErrRunFailed = errors.New("restore: restore run failed")
)

// Deps are the Runner's injected collaborators; every one is an interface so a
// restore can be exercised without OpenCloud, S3, or real keys.
type Deps struct {
	Spaces  cs3.SpaceReader
	Writer  cs3.SpaceWriter
	Configs spacecfg.Store
	Targets targets.Store
	Sealer  targets.CredSealer
	Keys    keys.Store
	Unwrap  keys.Wrapper
	Engine  snapshot.Engine
	Jobs    jobs.Store
	Locks   jobs.Locker
	// Logger receives operational detail: identifiers and counts, never key
	// material and never a user's file names.
	Logger *slog.Logger
	// Clock is injected for deterministic tests and names the restore folder.
	Clock Clock
	// RunTimeout bounds a background run started by StartRestore.
	RunTimeout time.Duration
}

// Clock supplies the current time.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// Runner executes restore runs.
type Runner struct {
	deps Deps
}

// Result summarises a completed restore. It carries no key material.
type Result struct {
	JobID      string
	SpaceID    string
	SnapshotID snapshot.SnapshotID
	// Folder is the space-relative folder the snapshot was restored into.
	Folder     string
	FileCount  int64
	TotalBytes int64
	StartedAt  time.Time
	FinishedAt time.Time
}

// NewRunner validates dependencies and constructs a Runner.
func NewRunner(d Deps) (*Runner, error) {
	missing := map[string]bool{
		"space reader":    d.Spaces == nil,
		"space writer":    d.Writer == nil,
		"config store":    d.Configs == nil,
		"target store":    d.Targets == nil,
		"credential seal": d.Sealer == nil,
		"key store":       d.Keys == nil,
		"key unwrapper":   d.Unwrap == nil,
		"snapshot engine": d.Engine == nil,
		"job store":       d.Jobs == nil,
		"job locker":      d.Locks == nil,
	}
	for name, isMissing := range missing {
		if isMissing {
			return nil, fmt.Errorf("restore: %s is required", name)
		}
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	if d.Clock == nil {
		d.Clock = systemClock{}
	}
	return &Runner{deps: d}, nil
}

// ListSnapshots returns the snapshots available for a Space, newest first. It
// is what the restore picker reads; the caller must already have verified the
// requester's membership.
func (r *Runner) ListSnapshots(ctx context.Context, spaceID string) ([]snapshot.Info, error) {
	space, err := r.resolveSpace(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	repo, err := r.openRepo(ctx, space.ID)
	if err != nil {
		return nil, err
	}
	defer keys.Zeroize(repo.DK)

	infos, err := r.deps.Engine.List(ctx, repo)
	if err != nil {
		r.deps.Logger.Error("could not list snapshots", "space", spaceID, "err", err)
		return nil, ErrTargetUnavailable
	}
	return infos, nil
}

// run is an accepted-but-not-yet-executed restore.
type run struct {
	space      cs3.Space
	snapshotID snapshot.SnapshotID
	job        jobs.Job
	folder     string
	startedAt  time.Time
	release    func()
}

// RunRestore performs one restore and waits for it to finish.
func (r *Runner) RunRestore(ctx context.Context, spaceID string, id snapshot.SnapshotID) (Result, error) {
	pending, err := r.begin(ctx, spaceID, id)
	if err != nil {
		return Result{}, err
	}
	defer pending.release()
	return r.finish(ctx, pending)
}

// StartRestore accepts a restore and runs it in the background, returning the
// job id to poll. Everything that can fail fast — the Space, the lock — is
// resolved before returning.
func (r *Runner) StartRestore(ctx context.Context, spaceID string, id snapshot.SnapshotID) (string, error) {
	pending, err := r.begin(ctx, spaceID, id)
	if err != nil {
		return "", err
	}

	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.runTimeout())
	go func() {
		defer cancel()
		defer pending.release()
		_, _ = r.finish(runCtx, pending)
	}()
	return pending.job.ID, nil
}

// begin takes the Space's run lock and opens a job record. On error the lock is
// already released.
func (r *Runner) begin(ctx context.Context, spaceID string, id snapshot.SnapshotID) (run, error) {
	if spaceID == "" {
		return run{}, ErrSpaceNotFound
	}
	if id == "" {
		return run{}, ErrSnapshotNotFound
	}

	// A restore and a backup for the same Space must not overlap: they share the
	// repository and the Space's contents.
	release, err := r.deps.Locks.Acquire(ctx, spaceID)
	if err != nil {
		if errors.Is(err, jobs.ErrLocked) {
			return run{}, ErrRunInProgress
		}
		return run{}, fmt.Errorf("restore: acquire run lock: %w", err)
	}

	space, err := r.resolveSpace(ctx, spaceID)
	if err != nil {
		release()
		return run{}, err
	}

	startedAt := r.deps.Clock.Now()
	job, err := r.deps.Jobs.Create(ctx, jobs.Job{
		SpaceID:    spaceID,
		Kind:       jobs.KindRestore,
		State:      jobs.StateRunning,
		SnapshotID: string(id),
	})
	if err != nil {
		release()
		return run{}, fmt.Errorf("restore: create job: %w", err)
	}

	return run{
		space:      space,
		snapshotID: id,
		job:        job,
		folder:     RestoreFolderName(startedAt),
		startedAt:  startedAt,
		release:    release,
	}, nil
}

// finish performs the restore and records the outcome.
func (r *Runner) finish(ctx context.Context, pending run) (Result, error) {
	stats, err := r.restoreSnapshot(ctx, pending)
	if err != nil {
		r.fail(ctx, pending.job.ID, pending.space.ID, err)
		return Result{}, err
	}

	if err := r.deps.Jobs.Finish(ctx, pending.job.ID, jobs.Outcome{
		State:      jobs.StateSucceeded,
		FileCount:  stats.files,
		TotalBytes: stats.bytes,
	}); err != nil {
		r.deps.Logger.Warn("could not record job success", "job", pending.job.ID, "err", err)
	}

	finishedAt := r.deps.Clock.Now()
	r.deps.Logger.Info("restore run succeeded",
		"space", pending.space.ID,
		"job", pending.job.ID,
		"snapshot", string(pending.snapshotID),
		"folder", pending.folder,
		"files", stats.files,
		"bytes", stats.bytes,
		"duration", finishedAt.Sub(pending.startedAt),
	)

	return Result{
		JobID:      pending.job.ID,
		SpaceID:    pending.space.ID,
		SnapshotID: pending.snapshotID,
		Folder:     pending.folder,
		FileCount:  stats.files,
		TotalBytes: stats.bytes,
		StartedAt:  pending.startedAt,
		FinishedAt: finishedAt,
	}, nil
}

// stats counts what a restore wrote.
type stats struct {
	files int64
	bytes int64
}

// restoreSnapshot streams a snapshot into the Space's restore folder.
func (r *Runner) restoreSnapshot(ctx context.Context, pending run) (stats, error) {
	repo, err := r.openRepo(ctx, pending.space.ID)
	if err != nil {
		return stats{}, err
	}
	// The plaintext Data Key exists only for this call (decisions.md #1).
	defer keys.Zeroize(repo.DK)

	// Create the restore root first, so a failure halfway leaves an obviously
	// partial folder rather than files scattered into the Space.
	if err := r.deps.Writer.MakeDir(ctx, pending.space, RestoreFolder); err != nil {
		return stats{}, r.wrapFailure(pending.space.ID, err)
	}
	if err := r.deps.Writer.MakeDir(ctx, pending.space, pending.folder); err != nil {
		return stats{}, r.wrapFailure(pending.space.ID, err)
	}

	var out stats
	err = r.deps.Engine.Walk(ctx, repo, pending.snapshotID, func(ctx context.Context, entry snapshot.RestoredEntry) error {
		dest := path.Join(pending.folder, entry.Path)
		if entry.IsDir {
			return r.deps.Writer.MakeDir(ctx, pending.space, dest)
		}
		if err := r.uploadEntry(ctx, pending.space, dest, entry); err != nil {
			return err
		}
		out.files++
		out.bytes += entry.Size
		return nil
	})
	if err != nil {
		if errors.Is(err, snapshot.ErrSnapshotNotFound) {
			return stats{}, ErrSnapshotNotFound
		}
		return stats{}, r.wrapFailure(pending.space.ID, err)
	}
	return out, nil
}

// uploadEntry streams one restored file into the Space.
func (r *Runner) uploadEntry(ctx context.Context, space cs3.Space, dest string, entry snapshot.RestoredEntry) error {
	rc, err := entry.Open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()

	return r.deps.Writer.Upload(ctx, space, dest, entry.Size, entry.ModTime, rc)
}

// wrapFailure logs the detailed cause (which may name a path) and returns a
// sanitized error. A user's file names must not travel into API responses or
// job records (AGENTS.md error rules).
func (r *Runner) wrapFailure(spaceID string, cause error) error {
	r.deps.Logger.Error("restore failed", "space", spaceID, "err", cause)
	return ErrRunFailed
}

// runTimeout bounds a background run.
func (r *Runner) runTimeout() time.Duration {
	if r.deps.RunTimeout > 0 {
		return r.deps.RunTimeout
	}
	return DefaultRunTimeout
}

// openRepo resolves the Space's target and Data Key into an openable repo. The
// caller owns and zeroizes the returned DK.
func (r *Runner) openRepo(ctx context.Context, spaceID string) (snapshot.Repo, error) {
	cfg, err := r.deps.Configs.Get(ctx, spaceID)
	if err != nil {
		var notFound spacecfg.ErrNotFound
		if errors.As(err, &notFound) {
			return snapshot.Repo{}, ErrNotConfigured
		}
		return snapshot.Repo{}, fmt.Errorf("restore: read space configuration: %w", err)
	}

	location, err := r.resolveTarget(ctx, cfg.TargetID)
	if err != nil {
		return snapshot.Repo{}, err
	}

	dk, err := r.unwrapDataKey(spaceID)
	if err != nil {
		return snapshot.Repo{}, err
	}
	return snapshot.Repo{
		Location: location,
		Space:    snapshot.SpaceRef{SpaceID: spaceID},
		DK:       dk,
	}, nil
}

// resolveSpace finds the Space the worker credential may read and write.
func (r *Runner) resolveSpace(ctx context.Context, spaceID string) (cs3.Space, error) {
	all, err := r.deps.Spaces.ListSpaces(ctx)
	if err != nil {
		return cs3.Space{}, fmt.Errorf("restore: list spaces: %w", err)
	}
	for _, s := range all {
		if s.ID == spaceID {
			return s, nil
		}
	}
	return cs3.Space{}, ErrSpaceNotFound
}

// resolveTarget loads the configured target and TW-unwraps its credentials.
func (r *Runner) resolveTarget(ctx context.Context, targetID string) (snapshot.Location, error) {
	if targetID == "" {
		return snapshot.Location{}, ErrNotConfigured
	}

	target, err := r.deps.Targets.GetTarget(ctx, targetID)
	if err != nil {
		var notFound targets.ErrNotFound
		if errors.As(err, &notFound) {
			return snapshot.Location{}, ErrTargetUnavailable
		}
		return snapshot.Location{}, fmt.Errorf("restore: read target: %w", err)
	}

	creds, err := r.deps.Sealer.Open(target.WrappedCreds)
	if err != nil {
		r.deps.Logger.Error("could not open target credentials", "target", targetID)
		return snapshot.Location{}, ErrTargetUnavailable
	}

	return snapshot.Location{
		Endpoint:        target.Endpoint,
		Region:          target.Region,
		Bucket:          target.Bucket,
		Prefix:          target.Prefix,
		AccessKeyID:     creds.AccessKeyID,
		SecretAccessKey: creds.SecretAccessKey,
		DisableTLS:      target.DisableTLS,
	}, nil
}

// unwrapDataKey recovers the Space's Data Key from its SRW envelope. Path B
// runs server-side on the user's behalf, so it uses the same server wrap the
// scheduler does; the user's Recovery Key is never involved (decisions.md #1).
func (r *Runner) unwrapDataKey(spaceID string) ([]byte, error) {
	wrapped, err := r.deps.Keys.GetSRW(spaceID)
	if err != nil {
		var notFound keys.ErrNotFound
		if errors.As(err, &notFound) {
			return nil, ErrNotConfigured
		}
		return nil, fmt.Errorf("restore: read server key envelope: %w", err)
	}

	dk, err := r.deps.Unwrap.UnwrapSRW(wrapped)
	if err != nil {
		r.deps.Logger.Error("could not unwrap data key", "space", spaceID)
		return nil, ErrNotConfigured
	}
	return dk, nil
}

// fail records a sanitized failure on the job record.
func (r *Runner) fail(ctx context.Context, jobID, spaceID string, cause error) {
	if err := r.deps.Jobs.Finish(ctx, jobID, jobs.Outcome{
		State: jobs.StateFailed,
		Error: userMessage(cause),
	}); err != nil {
		r.deps.Logger.Warn("could not record job failure", "job", jobID, "err", err)
	}
	r.deps.Logger.Error("restore run failed", "space", spaceID, "job", jobID, "err", cause)
}

// userMessage maps an internal error onto text safe to store and show.
func userMessage(err error) string {
	switch {
	case errors.Is(err, ErrNotConfigured):
		return "backup is not configured for this space"
	case errors.Is(err, ErrTargetUnavailable):
		return "the backup target is unavailable"
	case errors.Is(err, ErrSnapshotNotFound):
		return "no such snapshot"
	case errors.Is(err, ErrSpaceNotFound):
		return "space not found"
	case errors.Is(err, ErrRunInProgress):
		return "a run is already in progress"
	default:
		return "the restore run failed"
	}
}

// RestoreFolderName builds the space-relative folder a restore writes into:
// "Restore/<RFC3339 timestamp>". The timestamp is filename-safe on every
// platform the Space may later be synced to, so colons are replaced.
func RestoreFolderName(at time.Time) string {
	stamp := at.UTC().Format("2006-01-02T15-04-05Z")
	return path.Join(RestoreFolder, stamp)
}
