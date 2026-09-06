// Package backup orchestrates one backup run:
//
//	[CS3 read] -> [kopia snapshot/encrypt/dedup] -> [S3 target]
//
// It is the only place that holds a Space's plaintext Data Key and a target's
// plaintext S3 credentials at the same time, and it holds both only in memory
// for the duration of the run (decisions.md #1, #14). Neither is ever logged,
// returned, or persisted.
//
// The run sequence follows the phase-4 plan:
//
//  1. take the Space's run lock (no two runs per Space)
//  2. resolve the Space and its backup configuration
//  3. resolve the target and TW-unwrap its credentials
//  4. SRW-unwrap the Data Key
//  5. snapshot the Space into its per-Space kopia repo on the target
//  6. record the outcome in the job store
//  7. zeroize the Data Key
//
// Retention is *configured* here (a time-based keep-within window on the Space's
// configuration) but never *applied* here: prune and maintenance run as a
// separate job (decisions.md #9 Tier 1).
package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/objstore"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/takeout"
	"opencloud-backup-plugin/pkg/targets"
)

// Sentinel errors callers (the API) map onto status codes. Their messages are
// user-safe: they never name internal services, endpoints, or key material.
var (
	// ErrSpaceNotFound means the worker credential cannot see the Space.
	ErrSpaceNotFound = errors.New("backup: space not found")
	// ErrNotConfigured means the Space has no backup configuration or no keys.
	ErrNotConfigured = errors.New("backup: backup is not configured for this space")
	// ErrTargetUnavailable means the configured target is missing or its stored
	// credentials could not be opened.
	ErrTargetUnavailable = errors.New("backup: backup target is unavailable")
	// ErrRunInProgress means another run holds the Space's lock.
	ErrRunInProgress = errors.New("backup: a run is already in progress for this space")
	// ErrRunFailed means the snapshot itself failed.
	ErrRunFailed = errors.New("backup: snapshot run failed")
)

// Deps are the Runner's injected collaborators. Every one is an interface so a
// run can be exercised end to end without OpenCloud, S3, or real keys.
type Deps struct {
	Spaces  cs3.SpaceReader
	Configs spacecfg.Store
	Targets targets.Store
	Sealer  targets.CredSealer
	Keys    keys.Store
	Unwrap  keys.Wrapper
	Engine  snapshot.Engine
	Jobs    jobs.Store
	Locks   jobs.Locker
	// Envelopes publishes the Space's RK-wrapped Data Key envelope to the
	// target, so an admin Take-Out is self-contained and Path A works with
	// OpenCloud down. Optional: when nil, publication is skipped and Path A
	// depends on the envelope being exported some other way.
	Envelopes takeout.Publisher
	// Logger receives operational detail. It must never be handed key material;
	// the Runner only logs identifiers, counts and sanitized error text.
	Logger *slog.Logger
	// Clock is injected for deterministic tests.
	Clock Clock
	// RunTimeout bounds a background run started by StartBackup.
	// Zero uses DefaultRunTimeout.
	RunTimeout time.Duration
}

// DefaultRunTimeout bounds a background backup run. It is generous: a first run
// of a large Space uploads everything, and being killed halfway wastes the work.
const DefaultRunTimeout = 6 * time.Hour

// Clock supplies the current time.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// Runner executes backup runs.
type Runner struct {
	deps Deps
}

// Result summarises a completed run. It carries no key material.
type Result struct {
	JobID      string
	SpaceID    string
	SnapshotID snapshot.SnapshotID
	StartedAt  time.Time
	FinishedAt time.Time
	FileCount  int64
	TotalBytes int64
}

// NewRunner validates dependencies and constructs a Runner.
func NewRunner(d Deps) (*Runner, error) {
	missing := map[string]bool{
		"space reader":    d.Spaces == nil,
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
			return nil, fmt.Errorf("backup: %s is required", name)
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

// run is an accepted-but-not-yet-executed backup run.
type run struct {
	space     cs3.Space
	job       jobs.Job
	startedAt time.Time
	release   func()
}

// RunBackup performs one user-requested backup run and waits for it to finish.
// The HTTP trigger uses StartBackup; this is the synchronous variant.
func (r *Runner) RunBackup(ctx context.Context, spaceID string) (Result, error) {
	return r.run(ctx, spaceID, jobs.TriggerManual)
}

// RunScheduled performs one unattended run for the scheduler. It differs from
// RunBackup only in what the job record says started it — which is what lets
// the status board tell "your backup ran last night" from "you pressed the
// button". The credentials and the code path are identical: the SRW-wrapped
// Data Key (decisions.md #1), with no user session involved.
func (r *Runner) RunScheduled(ctx context.Context, spaceID string) (Result, error) {
	return r.run(ctx, spaceID, jobs.TriggerSchedule)
}

// run executes one backup run to completion.
func (r *Runner) run(ctx context.Context, spaceID string, trigger jobs.Trigger) (Result, error) {
	pending, err := r.begin(ctx, spaceID, trigger)
	if err != nil {
		return Result{}, err
	}
	defer pending.release()
	return r.finish(ctx, pending)
}

// StartBackup accepts a run and executes it in the background, returning the
// job id to poll. Everything that can fail fast — the Space's existence and the
// run lock — is resolved before returning, so the caller gets a real answer
// rather than a job that immediately fails.
func (r *Runner) StartBackup(ctx context.Context, spaceID string) (string, error) {
	pending, err := r.begin(ctx, spaceID, jobs.TriggerManual)
	if err != nil {
		return "", err
	}

	// The run must outlive the HTTP request that triggered it, but must not
	// outlive the process without bound.
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
func (r *Runner) begin(ctx context.Context, spaceID string, trigger jobs.Trigger) (run, error) {
	if spaceID == "" {
		return run{}, ErrSpaceNotFound
	}

	release, err := r.deps.Locks.Acquire(ctx, spaceID)
	if err != nil {
		if errors.Is(err, jobs.ErrLocked) {
			return run{}, ErrRunInProgress
		}
		return run{}, fmt.Errorf("backup: acquire run lock: %w", err)
	}

	// Resolve the Space before creating a job record so an unknown Space does
	// not litter the job history.
	space, err := r.resolveSpace(ctx, spaceID)
	if err != nil {
		release()
		return run{}, err
	}

	job, err := r.deps.Jobs.Create(ctx, jobs.Job{
		SpaceID: spaceID,
		Kind:    jobs.KindBackup,
		State:   jobs.StateRunning,
		Trigger: trigger,
	})
	if err != nil {
		release()
		return run{}, fmt.Errorf("backup: create job: %w", err)
	}

	return run{space: space, job: job, startedAt: r.deps.Clock.Now(), release: release}, nil
}

// finish performs the snapshot and records the outcome.
func (r *Runner) finish(ctx context.Context, pending run) (Result, error) {
	spaceID := pending.space.ID

	info, err := r.snapshotSpace(ctx, pending.space)
	if err != nil {
		r.fail(ctx, pending.job.ID, spaceID, err)
		return Result{}, err
	}

	if err := r.deps.Jobs.Finish(ctx, pending.job.ID, jobs.Outcome{
		State:      jobs.StateSucceeded,
		SnapshotID: string(info.ID),
		FileCount:  info.FileCount,
		TotalBytes: info.TotalBytes,
	}); err != nil {
		r.deps.Logger.Warn("could not record job success", "job", pending.job.ID, "err", err)
	}

	finishedAt := r.deps.Clock.Now()
	r.deps.Logger.Info("backup run succeeded",
		"space", spaceID,
		"job", pending.job.ID,
		"snapshot", string(info.ID),
		"files", info.FileCount,
		"bytes", info.TotalBytes,
		"duration", finishedAt.Sub(pending.startedAt),
	)

	return Result{
		JobID:      pending.job.ID,
		SpaceID:    spaceID,
		SnapshotID: info.ID,
		StartedAt:  pending.startedAt,
		FinishedAt: finishedAt,
		FileCount:  info.FileCount,
		TotalBytes: info.TotalBytes,
	}, nil
}

// runTimeout bounds a background run.
func (r *Runner) runTimeout() time.Duration {
	if r.deps.RunTimeout > 0 {
		return r.deps.RunTimeout
	}
	return DefaultRunTimeout
}

// snapshotSpace resolves the run's secrets, performs the snapshot, and disposes
// of the secrets. Keeping it in one function bounds the lifetime of both the
// Data Key and the target credentials to a single call frame.
func (r *Runner) snapshotSpace(ctx context.Context, space cs3.Space) (snapshot.Info, error) {
	cfg, err := r.deps.Configs.Get(ctx, space.ID)
	if err != nil {
		var notFound spacecfg.ErrNotFound
		if errors.As(err, &notFound) {
			return snapshot.Info{}, ErrNotConfigured
		}
		return snapshot.Info{}, fmt.Errorf("backup: read space configuration: %w", err)
	}

	target, err := r.resolveTarget(ctx, cfg.TargetID)
	if err != nil {
		return snapshot.Info{}, err
	}

	dk, err := r.unwrapDataKey(space.ID)
	if err != nil {
		return snapshot.Info{}, err
	}
	// The plaintext Data Key exists only for this call (decisions.md #1).
	defer keys.Zeroize(dk)

	// Publish the recovery envelope before the data it protects: a snapshot the
	// user cannot reach with their Recovery Key is worth less than no snapshot.
	r.publishEnvelope(ctx, space.ID, target)

	info, err := r.deps.Engine.Snapshot(ctx, snapshot.Repo{
		Location: target.location,
		Space:    snapshot.SpaceRef{SpaceID: space.ID},
		DK:       dk,
	}, NewSpaceSource(r.deps.Spaces, space))
	if err != nil {
		// Log the detail (which contains no secrets) but return a sanitized error.
		r.deps.Logger.Error("snapshot failed", "space", space.ID, "err", err)
		return snapshot.Info{}, fmt.Errorf("%w: %w", ErrRunFailed, err)
	}
	return info, nil
}

// resolveSpace finds the Space the worker credential may read.
func (r *Runner) resolveSpace(ctx context.Context, spaceID string) (cs3.Space, error) {
	all, err := r.deps.Spaces.ListSpaces(ctx)
	if err != nil {
		return cs3.Space{}, fmt.Errorf("backup: list spaces: %w", err)
	}
	for _, s := range all {
		if s.ID == spaceID {
			return s, nil
		}
	}
	return cs3.Space{}, ErrSpaceNotFound
}

// resolvedTarget is a target with its credentials opened for this run, in the
// two shapes the run needs: kopia's repository location and a plain object-store
// configuration for the key envelope. Both hold plaintext credentials and must
// never be logged.
type resolvedTarget struct {
	location snapshot.Location
	s3       objstore.S3Config
	prefix   string
}

// resolveTarget loads the configured target and TW-unwraps its credentials.
// Use snapshot.Location.Redacted() for diagnostics.
func (r *Runner) resolveTarget(ctx context.Context, targetID string) (resolvedTarget, error) {
	if targetID == "" {
		return resolvedTarget{}, ErrNotConfigured
	}

	target, err := r.deps.Targets.GetTarget(ctx, targetID)
	if err != nil {
		var notFound targets.ErrNotFound
		if errors.As(err, &notFound) {
			return resolvedTarget{}, ErrTargetUnavailable
		}
		return resolvedTarget{}, fmt.Errorf("backup: read target: %w", err)
	}

	creds, err := r.deps.Sealer.Open(target.WrappedCreds)
	if err != nil {
		// Never distinguish wrong-key from tampered, never echo the blob.
		r.deps.Logger.Error("could not open target credentials", "target", targetID)
		return resolvedTarget{}, ErrTargetUnavailable
	}

	return resolvedTarget{
		location: snapshot.Location{
			Endpoint: target.Endpoint,
			Region:   target.Region,
			Bucket:   target.Bucket,
			Prefix:   target.Prefix,
			// Go cannot zeroize strings; these copies die with the run and are
			// never logged or persisted (decisions.md #14, "where Go allows").
			AccessKeyID:     creds.AccessKeyID,
			SecretAccessKey: creds.SecretAccessKey,
			DisableTLS:      target.DisableTLS,
		},
		s3: objstore.S3Config{
			Endpoint:        target.Endpoint,
			Region:          target.Region,
			Bucket:          target.Bucket,
			AccessKeyID:     creds.AccessKeyID,
			SecretAccessKey: creds.SecretAccessKey,
			UsePathStyle:    target.UsePathStyle,
			DisableTLS:      target.DisableTLS,
		},
		prefix: target.Prefix,
	}, nil
}

// publishEnvelope copies the Space's RK-wrapped Data Key envelope to the target
// so an admin Take-Out is self-contained (Path A works with OpenCloud down).
//
// The envelope is ciphertext the server cannot open, so this does not weaken the
// trust model. A failure here is logged but does not fail the run: the snapshot
// itself is still valid and still restorable through Path B, and refusing to
// back a Space up because one small object could not be written would trade a
// real protection for a theoretical one.
func (r *Runner) publishEnvelope(ctx context.Context, spaceID string, target resolvedTarget) {
	if r.deps.Envelopes == nil {
		return
	}

	wrapped, err := r.deps.Keys.GetRK(spaceID)
	if err != nil {
		var notFound keys.ErrNotFound
		if errors.As(err, &notFound) {
			r.deps.Logger.Warn("no recovery envelope stored for space; take-out will not be self-contained",
				"space", spaceID)
			return
		}
		r.deps.Logger.Warn("could not read recovery envelope", "space", spaceID, "err", err)
		return
	}

	if err := r.deps.Envelopes.Publish(ctx, takeout.PublishTarget{
		S3:     target.s3,
		Prefix: target.prefix,
	}, spaceID, wrapped.Blob); err != nil {
		// The error describes the target, never the envelope contents.
		r.deps.Logger.Error("could not publish recovery envelope to target",
			"space", spaceID, "err", err)
	}
}

// unwrapDataKey recovers the Space's Data Key from its SRW envelope. This is the
// unattended worker's path (decisions.md #1); the plaintext RK is never involved.
func (r *Runner) unwrapDataKey(spaceID string) ([]byte, error) {
	wrapped, err := r.deps.Keys.GetSRW(spaceID)
	if err != nil {
		var notFound keys.ErrNotFound
		if errors.As(err, &notFound) {
			return nil, ErrNotConfigured
		}
		return nil, fmt.Errorf("backup: read server key envelope: %w", err)
	}

	dk, err := r.deps.Unwrap.UnwrapSRW(wrapped)
	if err != nil {
		// The failure reason would describe key material; keep it opaque.
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
	r.deps.Logger.Error("backup run failed", "space", spaceID, "job", jobID, "err", cause)
}

// userMessage maps an internal error onto text safe to store and show. Anything
// unrecognised collapses to a generic message rather than leaking detail
// (AGENTS.md error rules).
func userMessage(err error) string {
	switch {
	case errors.Is(err, ErrNotConfigured):
		return "backup is not configured for this space"
	case errors.Is(err, ErrTargetUnavailable):
		return "the backup target is unavailable"
	case errors.Is(err, ErrSpaceNotFound):
		return "space not found"
	case errors.Is(err, ErrRunInProgress):
		return "a run is already in progress"
	default:
		return "the backup run failed"
	}
}
