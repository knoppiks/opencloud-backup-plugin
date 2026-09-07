package snapshot

// KopiaEngine: the concrete Engine, wrapping kopia's importable API exactly as
// pinned by Phase-0 Spike 2 (phase-0-findings.md). It owns the repository
// lifecycle — including kopia's on-disk config/cache, which is created per run
// and removed afterwards so the worker stays stateless.
//
// Key handling: kopia takes the repository password as a Go string, so the DK
// is unavoidably copied into an immutable string for the duration of a call.
// That copy is confined to this file, never logged, and never returned in an
// error; the caller still owns and zeroizes the []byte.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kopia/kopia/fs"
	"github.com/kopia/kopia/repo"
	"github.com/kopia/kopia/repo/blob"
	"github.com/kopia/kopia/repo/content"
	"github.com/kopia/kopia/repo/maintenance"
	"github.com/kopia/kopia/repo/manifest"
	ksnapshot "github.com/kopia/kopia/snapshot"
	"github.com/kopia/kopia/snapshot/policy"
	"github.com/kopia/kopia/snapshot/restore"
	"github.com/kopia/kopia/snapshot/snapshotfs"
	"github.com/kopia/kopia/snapshot/upload"
)

const (
	// defaultHost / defaultUser identify snapshot sources inside a repo. They are
	// constants, not machine values: the same Space must map to the same kopia
	// source no matter which worker pod runs the backup, or the snapshot chain
	// (and therefore dedup) would fragment.
	defaultHost = "opencloud"
	defaultUser = "backup"

	// sourcePathPrefix is the kopia SourceInfo.Path prefix for a Space.
	sourcePathPrefix = "/spaces/"
)

// rootModTime is the modification time reported for the snapshot's root
// directory. It is constant so the root never looks changed between runs; the
// Space's own files carry their real mtimes.
var rootModTime = time.Unix(0, 0).UTC()

// ErrSnapshotNotFound is returned when a snapshot id is not present in the repo.
var ErrSnapshotNotFound = errors.New("snapshot: no such snapshot")

// EngineOptions configures a KopiaEngine. The zero value is usable.
type EngineOptions struct {
	// Parallelism is the number of files hashed/uploaded concurrently.
	// 0 uses kopia's default (number of CPUs).
	Parallelism int
	// WorkDir is the parent directory for the per-run kopia config and cache.
	// Empty uses the OS temp directory.
	WorkDir string
	// MaintenanceSafety controls how aggressively Prune's garbage collection
	// reclaims recently-unreferenced content. Defaults to maintenance.SafetyFull,
	// which is safe to run concurrently with snapshotting.
	MaintenanceSafety *maintenance.SafetyParameters
	// CheckpointInterval is how often kopia flushes a partial tree mid-upload so
	// a long run does not lose all its work on a crash. 0 uses kopia's default
	// (45 minutes), which is also kopia's maximum. Tests drive it down to
	// seconds; production has no reason to change it.
	CheckpointInterval time.Duration
}

// KopiaEngine implements Engine on top of kopia.
type KopiaEngine struct {
	opener StorageOpener
	opts   EngineOptions
}

var _ Engine = (*KopiaEngine)(nil)

// NewEngine constructs a KopiaEngine over the given storage opener.
func NewEngine(opener StorageOpener, opts EngineOptions) (*KopiaEngine, error) {
	if opener == nil {
		return nil, fmt.Errorf("snapshot: storage opener required")
	}
	if opts.CheckpointInterval < 0 {
		return nil, fmt.Errorf("snapshot: checkpoint interval must not be negative")
	}
	// kopia rejects a larger interval at upload time; failing here turns a
	// misconfiguration into a startup error instead of a failed backup.
	if opts.CheckpointInterval > upload.DefaultCheckpointInterval {
		return nil, fmt.Errorf("snapshot: checkpoint interval must not exceed %v", upload.DefaultCheckpointInterval)
	}
	return &KopiaEngine{opener: opener, opts: opts}, nil
}

// Snapshot creates a snapshot of src in the Space's repository, initialising the
// repository on first use.
func (e *KopiaEngine) Snapshot(ctx context.Context, r Repo, src Source) (Info, error) {
	if src == nil {
		return Info{}, fmt.Errorf("snapshot: source required")
	}

	var out Info
	err := e.withRepo(ctx, r, true, func(ctx context.Context, rep repo.Repository) error {
		si := sourceInfo(r.Space)

		err := repo.WriteSession(ctx, rep, repo.WriteSessionOptions{Purpose: "backup"},
			func(ctx context.Context, w repo.RepositoryWriter) error {
				if err := disableCountBasedRetention(ctx, w, si); err != nil {
					return err
				}

				policyTree, err := policy.TreeForSource(ctx, w, si)
				if err != nil {
					return fmt.Errorf("snapshot: policy tree: %w", err)
				}
				// Feeding previous manifests enables kopia's hash cache: an
				// unchanged file is reused without being re-read from the source.
				// Incomplete manifests are deliberately not filtered out here:
				// they are a legitimate cache for a run resuming after a crash,
				// and this is the one place they are read rather than served.
				previous, err := ksnapshot.ListSnapshots(ctx, w, si)
				if err != nil {
					return fmt.Errorf("snapshot: list previous snapshots: %w", err)
				}

				u := upload.NewUploader(w)
				u.ParallelUploads = e.opts.Parallelism
				if e.opts.CheckpointInterval > 0 {
					u.CheckpointInterval = e.opts.CheckpointInterval
				}
				// kopia's ignore conventions (.kopiaignore files, CACHEDIR.TAG
				// markers) let the tree being backed up decide what is backed
				// up. That is right for a laptop, where the person writing the
				// rules is the person running the backup, and wrong for a user's
				// Space, where anyone who can put a file in it could otherwise
				// silence the backup of everything around it. A Space's contents
				// are never a policy input.
				u.DisableIgnoreRules = true

				man, err := u.Upload(ctx, rootEntry(src, rootModTime), policyTree, si, previous...)
				if err != nil {
					return fmt.Errorf("snapshot: upload: %w", err)
				}
				// kopia records per-entry read failures in the manifest instead of
				// failing the upload. A backup that silently dropped files must
				// not be reported as a success, so the manifest is not saved.
				if err := assertComplete(man); err != nil {
					return err
				}
				if _, err := ksnapshot.SaveSnapshot(ctx, w, man); err != nil {
					return fmt.Errorf("snapshot: save manifest: %w", err)
				}
				if err := deleteIncomplete(ctx, w, si); err != nil {
					return err
				}
				out = toInfo(man)
				return nil
			})
		if err != nil {
			// The run failed and its error is the one worth reporting, but the
			// checkpoints kopia flushed along the way outlive the rolled-back
			// session and must not be left looking like snapshots. Best effort:
			// every consumer filters them out and Prune always expires them.
			_ = discardIncomplete(context.WithoutCancel(ctx), rep, si)
			return err
		}
		return nil
	})
	if err != nil {
		return Info{}, err
	}
	return out, nil
}

// discardIncomplete removes a source's incomplete manifests in a session of its
// own. It exists for the failure path: kopia's checkpoints flush themselves, so
// they survive the rollback of the backup's own write session.
func discardIncomplete(ctx context.Context, rep repo.Repository, si ksnapshot.SourceInfo) error {
	return repo.WriteSession(ctx, rep, repo.WriteSessionOptions{Purpose: "discard-incomplete"},
		func(ctx context.Context, w repo.RepositoryWriter) error {
			return deleteIncomplete(ctx, w, si)
		})
}

// deleteIncomplete deletes every incomplete manifest of a source.
//
// kopia saves a partial tree every CheckpointInterval so a long upload does not
// start from scratch after a crash. Once written, such a manifest is
// indistinguishable from a finished snapshot to anything that lists the repo, so
// the run that wrote it removes it when it ends — success or failure.
//
// This deletes every incomplete manifest, not just the current run's: a Space is
// backed up under a run lock, so no other run can be writing one, and a manifest
// left behind by a killed process is the same garbage. The content it referenced
// is reclaimed by the next maintenance pass.
func deleteIncomplete(ctx context.Context, w repo.RepositoryWriter, si ksnapshot.SourceInfo) error {
	mans, err := ksnapshot.ListSnapshots(ctx, w, si)
	if err != nil {
		return fmt.Errorf("snapshot: list snapshots: %w", err)
	}
	for _, m := range mans {
		if m.IncompleteReason == "" {
			continue
		}
		if err := w.DeleteManifest(ctx, m.ID); err != nil {
			return fmt.Errorf("snapshot: delete incomplete manifest: %w", err)
		}
	}
	return nil
}

// completeOnly keeps the manifests that describe a whole Space.
//
// A manifest with an IncompleteReason is a mid-upload checkpoint or a cancelled
// run: a subset of the Space, saved so work is not lost. Serving one as a
// snapshot would offer a partial restore as if it were a full one, so every
// consumer of a manifest list outside the backup itself goes through here.
func completeOnly(mans []*ksnapshot.Manifest) []*ksnapshot.Manifest {
	out := make([]*ksnapshot.Manifest, 0, len(mans))
	for _, m := range mans {
		if m.IncompleteReason == "" {
			out = append(out, m)
		}
	}
	return out
}

// ErrIncompleteSnapshot is returned when the source could not be read in full.
var ErrIncompleteSnapshot = errors.New("snapshot: source could not be read completely")

// assertComplete rejects a manifest that does not cover the whole source.
// The error carries counts only — never the failing paths, which name a user's
// files (AGENTS.md error rules).
func assertComplete(man *ksnapshot.Manifest) error {
	if man.IncompleteReason != "" {
		return fmt.Errorf("%w: %s", ErrIncompleteSnapshot, man.IncompleteReason)
	}
	if s := man.RootEntry.DirSummary; s != nil && s.FatalErrorCount > 0 {
		return fmt.Errorf("%w: %d entries failed to read", ErrIncompleteSnapshot, s.FatalErrorCount)
	}
	return nil
}

// List returns the Space's snapshots, newest first.
func (e *KopiaEngine) List(ctx context.Context, r Repo) ([]Info, error) {
	var out []Info
	err := e.withRepo(ctx, r, false, func(ctx context.Context, rep repo.Repository) error {
		mans, err := ksnapshot.ListSnapshots(ctx, rep, sourceInfo(r.Space))
		if err != nil {
			return fmt.Errorf("snapshot: list snapshots: %w", err)
		}
		mans = completeOnly(mans)
		out = make([]Info, 0, len(mans))
		for _, m := range mans {
			out = append(out, toInfo(m))
		}
		sort.Slice(out, func(i, j int) bool { return out[i].StartTime.After(out[j].StartTime) })
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RestoreAll materialises a whole snapshot into outDir.
func (e *KopiaEngine) RestoreAll(ctx context.Context, r Repo, id SnapshotID, outDir string) error {
	return e.restore(ctx, r, id, "", outDir)
}

// RestoreFile materialises a single file or subtree into outDir.
func (e *KopiaEngine) RestoreFile(ctx context.Context, r Repo, id SnapshotID, relPath, outDir string) error {
	if strings.TrimSpace(relPath) == "" {
		return fmt.Errorf("snapshot: restore path required")
	}
	return e.restore(ctx, r, id, relPath, outDir)
}

// restore is the shared body of RestoreAll/RestoreFile. An empty relPath
// restores the snapshot root.
func (e *KopiaEngine) restore(ctx context.Context, r Repo, id SnapshotID, relPath, outDir string) error {
	if outDir == "" {
		return fmt.Errorf("snapshot: restore target required")
	}

	return e.withRepo(ctx, r, false, func(ctx context.Context, rep repo.Repository) error {
		man, err := findManifest(ctx, rep, sourceInfo(r.Space), id)
		if err != nil {
			return err
		}
		root, err := snapshotfs.SnapshotRoot(rep, man)
		if err != nil {
			return fmt.Errorf("snapshot: open snapshot root: %w", err)
		}

		entry := root
		if relPath != "" {
			elements := strings.Split(path.Clean(strings.Trim(relPath, "/")), "/")
			entry, err = snapshotfs.GetNestedEntry(ctx, root, elements)
			if err != nil {
				return fmt.Errorf("snapshot: locate %q in snapshot: %w", relPath, err)
			}
		}

		out := &restore.FilesystemOutput{
			TargetPath:           outDir,
			OverwriteDirectories: true,
			OverwriteFiles:       true,
			// Ownership is out of backup scope (decisions.md #4) and the source
			// entries carry no real uid/gid, so restoring it would only attempt
			// (and fail) a chown to root.
			SkipOwners: true,
		}
		// Init sets up the output's stream copier; restore.Entry does not do it
		// and nil-derefs without it (phase-0-findings.md Spike 2).
		if err := out.Init(ctx); err != nil {
			return fmt.Errorf("snapshot: init restore output: %w", err)
		}
		// The default depth of 0 writes shallow .kopia-entry placeholders instead
		// of real files (phase-0-findings.md Spike 2).
		if _, err := restore.Entry(ctx, rep, out, entry, restore.Options{
			RestoreDirEntryAtDepth: math.MaxInt32,
		}); err != nil {
			return fmt.Errorf("snapshot: restore: %w", err)
		}
		return nil
	})
}

// Walk streams a snapshot's tree without writing anything to disk. It is the
// restore-into-OpenCloud path (Path B): entries are handed to fn one at a time
// and file bytes are pulled from the repository on demand.
//
// Entries out of backup scope (symlinks, unknown types — decisions.md #4) are
// skipped rather than reported, so a caller never has to know kopia's type set.
func (e *KopiaEngine) Walk(ctx context.Context, r Repo, id SnapshotID, fn func(context.Context, RestoredEntry) error) error {
	if fn == nil {
		return fmt.Errorf("snapshot: walk callback required")
	}

	return e.withRepo(ctx, r, false, func(ctx context.Context, rep repo.Repository) error {
		man, err := findManifest(ctx, rep, sourceInfo(r.Space), id)
		if err != nil {
			return err
		}
		root, err := snapshotfs.SnapshotRoot(rep, man)
		if err != nil {
			return fmt.Errorf("snapshot: open snapshot root: %w", err)
		}
		dir, ok := root.(fs.Directory)
		if !ok {
			return fmt.Errorf("snapshot: snapshot root is not a directory")
		}
		return walkDir(ctx, dir, "", fn)
	})
}

// walkDir recurses through one directory of a snapshot.
func walkDir(ctx context.Context, dir fs.Directory, prefix string, fn func(context.Context, RestoredEntry) error) error {
	return fs.IterateEntries(ctx, dir, func(ctx context.Context, entry fs.Entry) error {
		rel := path.Join(prefix, entry.Name())
		switch t := entry.(type) {
		case fs.Directory:
			if err := fn(ctx, RestoredEntry{
				Path:    rel,
				IsDir:   true,
				ModTime: entry.ModTime(),
			}); err != nil {
				return err
			}
			return walkDir(ctx, t, rel, fn)
		case fs.File:
			return fn(ctx, RestoredEntry{
				Path:    rel,
				Size:    entry.Size(),
				ModTime: entry.ModTime(),
				Open: func(ctx context.Context) (io.ReadCloser, error) {
					return t.Open(ctx)
				},
			})
		default:
			return nil
		}
	})
}

// Prune applies time-based keep-within retention and reclaims space.
//
// kopia has no native keep-within (its policy engine is count-based only), so
// retention is implemented here: list manifests, delete those older than
// now-window, then run full maintenance to garbage-collect unreferenced content
// (decisions.md #10; phase-0-findings.md Spike 2).
//
// The newest complete snapshot is never deleted, even when it is older than the
// window: a Space that stopped being backed up must not silently lose its last
// copy. Incomplete manifests are always deleted — a run that ends cleans up its
// own, so any left here belong to a process that was killed.
func (e *KopiaEngine) Prune(ctx context.Context, r Repo, window time.Duration) error {
	if window <= 0 {
		return fmt.Errorf("snapshot: retention window must be positive")
	}

	return e.withRepo(ctx, r, false, func(ctx context.Context, rep repo.Repository) error {
		si := sourceInfo(r.Space)

		err := repo.WriteSession(ctx, rep, repo.WriteSessionOptions{Purpose: "prune"},
			func(ctx context.Context, w repo.RepositoryWriter) error {
				mans, err := ksnapshot.ListSnapshots(ctx, w, si)
				if err != nil {
					return fmt.Errorf("snapshot: list snapshots: %w", err)
				}
				for _, id := range expiredManifests(mans, w.Time().Add(-window)) {
					if err := w.DeleteManifest(ctx, id); err != nil {
						return fmt.Errorf("snapshot: delete expired manifest: %w", err)
					}
				}
				return nil
			})
		if err != nil {
			return err
		}
		return e.runMaintenance(ctx, rep)
	})
}

// expiredManifests returns the manifests to delete: everything incomplete, plus
// every complete snapshot started before cutoff except the newest one.
//
// The protection is on the newest *complete* snapshot, which is the newest one a
// user could actually restore. Protecting the newest manifest of any kind would
// let a mid-upload checkpoint stand in for it and the last full backup be
// deleted underneath it.
func expiredManifests(mans []*ksnapshot.Manifest, cutoff time.Time) []manifest.ID {
	var expired []manifest.ID

	complete := completeOnly(mans)
	for _, m := range mans {
		if m.IncompleteReason != "" {
			expired = append(expired, m.ID)
		}
	}

	if len(complete) <= 1 {
		return expired
	}
	sort.Slice(complete, func(i, j int) bool {
		return complete[i].StartTime.ToTime().After(complete[j].StartTime.ToTime())
	})
	for _, m := range complete[1:] {
		if m.StartTime.ToTime().Before(cutoff) {
			expired = append(expired, m.ID)
		}
	}
	return expired
}

// runMaintenance garbage-collects content no longer referenced by any manifest.
// Only the maintenance owner may run it, so ownership is claimed on first use
// (phase-0-findings.md Spike 2, "Maintenance ownership is a real concept").
func (e *KopiaEngine) runMaintenance(ctx context.Context, rep repo.Repository) error {
	dr, ok := rep.(repo.DirectRepository)
	if !ok {
		return fmt.Errorf("snapshot: repository does not support maintenance")
	}

	safety := maintenance.SafetyFull
	if e.opts.MaintenanceSafety != nil {
		safety = *e.opts.MaintenanceSafety
	}

	err := repo.DirectWriteSession(ctx, dr, repo.WriteSessionOptions{Purpose: "prune-gc"},
		func(ctx context.Context, dw repo.DirectRepositoryWriter) error {
			p, err := maintenance.GetParams(ctx, dw)
			if err != nil {
				return fmt.Errorf("snapshot: maintenance params: %w", err)
			}
			if p.Owner == "" {
				def := maintenance.DefaultParams()
				def.Owner = dw.ClientOptions().UsernameAtHost()
				if err := maintenance.SetParams(ctx, dw, &def); err != nil {
					return fmt.Errorf("snapshot: claim maintenance ownership: %w", err)
				}
			}
			return maintenance.RunExclusive(ctx, dw, maintenance.ModeFull, true,
				func(ctx context.Context, rp maintenance.RunParameters) error {
					return maintenance.Run(ctx, rp, safety)
				})
		})
	if err != nil {
		return fmt.Errorf("snapshot: maintenance: %w", err)
	}
	return nil
}

// withRepo runs fn against an open repository, owning the whole lifecycle:
// blob storage, optional initialisation, a throwaway local config/cache, and
// cleanup. A stateless worker gets a fresh cache per run
// (phase-0-findings.md Spike 2, "Repo connect is stateful on local disk").
func (e *KopiaEngine) withRepo(ctx context.Context, r Repo, createIfMissing bool, fn func(context.Context, repo.Repository) error) (err error) {
	if len(r.DK) == 0 {
		return fmt.Errorf("snapshot: data key required")
	}
	if r.Space.SpaceID == "" {
		return fmt.Errorf("snapshot: space id required")
	}

	st, err := e.opener.Open(ctx, r, createIfMissing)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := st.Close(context.WithoutCancel(ctx)); cerr != nil && err == nil {
			err = fmt.Errorf("snapshot: close storage: %w", cerr)
		}
	}()

	// kopia takes the password as a string; this copy is confined to this call.
	password := string(r.DK)

	if createIfMissing {
		if err := initializeIfNeeded(ctx, st, password); err != nil {
			return err
		}
	}

	workDir, err := os.MkdirTemp(e.opts.WorkDir, "kopia-run-*")
	if err != nil {
		return fmt.Errorf("snapshot: create work dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	configFile := filepath.Join(workDir, "repository.config")
	if err := repo.Connect(ctx, configFile, st, password, &repo.ConnectOptions{
		ClientOptions: repo.ClientOptions{
			Hostname:    defaultHost,
			Username:    defaultUser,
			Description: "opencloud-backup space " + r.Space.SpaceID,
		},
		CachingOptions: content.CachingOptions{
			CacheDirectory: filepath.Join(workDir, "cache"),
		},
	}); err != nil {
		return fmt.Errorf("snapshot: connect repository: %w", err)
	}

	rep, err := repo.Open(ctx, configFile, password, &repo.Options{})
	if err != nil {
		return fmt.Errorf("snapshot: open repository: %w", err)
	}
	defer func() {
		if cerr := rep.Close(context.WithoutCancel(ctx)); cerr != nil && err == nil {
			err = fmt.Errorf("snapshot: close repository: %w", cerr)
		}
	}()

	return fn(ctx, rep)
}

// initializeIfNeeded creates the repository on first use and treats an existing
// repository as success.
func initializeIfNeeded(ctx context.Context, st blob.Storage, password string) error {
	err := repo.Initialize(ctx, st, &repo.NewRepositoryOptions{}, password)
	switch {
	case err == nil, errors.Is(err, repo.ErrAlreadyInitialized):
		return nil
	default:
		return fmt.Errorf("snapshot: initialize repository: %w", err)
	}
}

// disableCountBasedRetention pins a source-level policy that never expires a
// snapshot by count. kopia's policy engine is count-based only, and its
// checkpointing path applies it automatically mid-upload; decisions.md #10
// forbids count-based retention, so every counter is set to zero — which kopia
// interprets as "keep everything". Expiry happens exclusively in Prune.
func disableCountBasedRetention(ctx context.Context, w repo.RepositoryWriter, si ksnapshot.SourceInfo) error {
	existing, err := policy.GetDefinedPolicy(ctx, w, si)
	if err != nil && !errors.Is(err, policy.ErrPolicyNotFound) {
		return fmt.Errorf("snapshot: read policy: %w", err)
	}

	if existing != nil && isNeverExpireByCount(existing.RetentionPolicy) {
		return nil
	}

	pol := &policy.Policy{}
	if existing != nil {
		pol = existing
	}
	pol.RetentionPolicy = neverExpireByCount()
	if err := policy.SetPolicy(ctx, w, si, pol); err != nil {
		return fmt.Errorf("snapshot: set policy: %w", err)
	}
	return nil
}

// isNeverExpireByCount reports whether every count-based retention counter is
// explicitly zero.
func isNeverExpireByCount(rp policy.RetentionPolicy) bool {
	for _, v := range []*policy.OptionalInt{
		rp.KeepLatest, rp.KeepHourly, rp.KeepDaily,
		rp.KeepWeekly, rp.KeepMonthly, rp.KeepAnnual,
	} {
		if v == nil || *v != 0 {
			return false
		}
	}
	return true
}

// neverExpireByCount builds the retention policy that keeps every snapshot.
// With all counters zero kopia's EffectiveKeepLatest becomes MaxInt, so every
// snapshot is retained by the "latest" rule.
func neverExpireByCount() policy.RetentionPolicy {
	zero := policy.OptionalInt(0)
	return policy.RetentionPolicy{
		KeepLatest:  &zero,
		KeepHourly:  &zero,
		KeepDaily:   &zero,
		KeepWeekly:  &zero,
		KeepMonthly: &zero,
		KeepAnnual:  &zero,
	}
}

// findManifest resolves a SnapshotID within the Space's source. Incomplete
// manifests are not resolvable: nothing may be restored from a partial tree.
func findManifest(ctx context.Context, rep repo.Repository, si ksnapshot.SourceInfo, id SnapshotID) (*ksnapshot.Manifest, error) {
	if id == "" {
		return nil, fmt.Errorf("snapshot: snapshot id required")
	}
	mans, err := ksnapshot.ListSnapshots(ctx, rep, si)
	if err != nil {
		return nil, fmt.Errorf("snapshot: list snapshots: %w", err)
	}
	for _, m := range completeOnly(mans) {
		if SnapshotID(m.ID) == id {
			return m, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrSnapshotNotFound, id)
}

// sourceInfo maps a Space to kopia's source identity. It is deterministic so the
// snapshot chain (and therefore dedup) is stable across worker restarts.
func sourceInfo(ref SpaceRef) ksnapshot.SourceInfo {
	return ksnapshot.SourceInfo{
		Host:     defaultHost,
		UserName: defaultUser,
		Path:     sourcePathPrefix + ref.SpaceID,
	}
}

// toInfo projects a kopia manifest onto our key-material-free Info.
func toInfo(m *ksnapshot.Manifest) Info {
	return Info{
		ID:         SnapshotID(m.ID),
		StartTime:  m.StartTime.ToTime(),
		FileCount:  int64(m.Stats.TotalFileCount),
		TotalBytes: m.Stats.TotalFileSize,
	}
}
