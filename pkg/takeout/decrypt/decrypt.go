// Package decrypt is the user-side half of Path A: unwrap the Data Key with the
// Recovery Key and restore a snapshot out of a Take-Out directory.
//
// It is a package of its own so that the split of powers is structural rather
// than conventional: the admin's `takeout` binary links the parent package
// (extract, verify, publish) and never this one, and a test in cmd/takeout
// asserts that dependency graph (decisions.md #2, #15).
//
// Exactly what that buys, stated honestly: the admin's binary contains no code
// that turns an envelope plus a Recovery Key into a Data Key, and none that
// turns a repository into files. It does still link pkg/keys, because extraction
// has to read an envelope's public header to record what it copied; the unwrap
// functions in that package are unreferenced there, which the same test file
// checks by reading the source.
//
// Everything here is offline by construction — a local directory, a local kopia
// repository, no network client of any kind. This is the family's last resort,
// so it stays deliberately small: parse manifest, unwrap envelope, open
// repository, restore.
package decrypt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/takeout"
)

// Errors this package adds to the Take-Out set. Like those, they stay coarse on
// purpose: an unwrap failure must not hint at which part of the input was wrong,
// and no error ever carries key material.
var (
	// ErrWrongRecoveryKey means the Recovery Key does not open this Take-Out.
	ErrWrongRecoveryKey = errors.New("takeout: key does not match this take-out")
	// ErrUnsupportedEnvelope means the envelope's format version is newer than
	// this tool understands.
	ErrUnsupportedEnvelope = errors.New("takeout: unsupported key envelope version")
)

// Snapshot is one restorable snapshot inside a Take-Out.
type Snapshot struct {
	ID         string
	StartTime  time.Time
	FileCount  int64
	TotalBytes int64
}

// Result summarises a completed offline restore.
type Result struct {
	SpaceID    string
	SnapshotID string
	StartTime  time.Time
	OutDir     string
}

// Options configures an offline restore.
type Options struct {
	// Dir is the Take-Out directory.
	Dir string
	// RecoveryKey is the raw Recovery Key secret (as returned by
	// keys.DecodeRecoveryKey). The caller owns and zeroizes the buffer.
	RecoveryKey []byte
	// OutDir receives the restored files. It is only created once the Recovery
	// Key has been proven correct, so a wrong key leaves nothing behind.
	OutDir string
	// SnapshotID selects a snapshot; empty restores the newest one.
	SnapshotID string
	// WorkDir is the parent for kopia's throwaway cache. Empty uses the OS temp
	// directory.
	WorkDir string
}

// ListSnapshots returns the snapshots inside a Take-Out, newest first. It needs
// the Recovery Key because snapshot metadata lives inside the encrypted
// repository — nothing about a backup is readable without it.
func ListSnapshots(ctx context.Context, dir string, recoveryKey []byte, workDir string) ([]Snapshot, error) {
	m, err := takeout.ReadManifest(dir)
	if err != nil {
		return nil, err
	}

	dk, err := unwrapDataKey(dir, m, recoveryKey)
	if err != nil {
		return nil, err
	}
	defer keys.Zeroize(dk)

	engine, repo, err := openRepo(dir, m, dk, workDir)
	if err != nil {
		return nil, err
	}

	infos, err := engine.List(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("%w: repository could not be read", takeout.ErrCorrupt)
	}

	out := make([]Snapshot, 0, len(infos))
	for _, info := range infos {
		out = append(out, Snapshot{
			ID:         string(info.ID),
			StartTime:  info.StartTime,
			FileCount:  info.FileCount,
			TotalBytes: info.TotalBytes,
		})
	}
	return out, nil
}

// Decrypt restores a snapshot from a Take-Out into OutDir.
func Decrypt(ctx context.Context, opts Options) (Result, error) {
	if opts.OutDir == "" {
		return Result{}, fmt.Errorf("takeout: output directory is required")
	}

	m, err := takeout.ReadManifest(opts.Dir)
	if err != nil {
		return Result{}, err
	}

	dk, err := unwrapDataKey(opts.Dir, m, opts.RecoveryKey)
	if err != nil {
		return Result{}, err
	}
	defer keys.Zeroize(dk)

	engine, repo, err := openRepo(opts.Dir, m, dk, opts.WorkDir)
	if err != nil {
		return Result{}, err
	}

	infos, err := engine.List(ctx, repo)
	if err != nil {
		return Result{}, fmt.Errorf("%w: repository could not be read", takeout.ErrCorrupt)
	}
	chosen, err := selectSnapshot(infos, opts.SnapshotID)
	if err != nil {
		return Result{}, err
	}

	// Only now — with the key proven and the snapshot resolved — does anything
	// get written to the user's disk.
	if err := os.MkdirAll(opts.OutDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("takeout: create output directory: %w", err)
	}
	if err := engine.RestoreAll(ctx, repo, chosen.ID, opts.OutDir); err != nil {
		return Result{}, fmt.Errorf("takeout: restore failed: %w", err)
	}

	return Result{
		SpaceID:    m.SpaceID,
		SnapshotID: string(chosen.ID),
		StartTime:  chosen.StartTime,
		OutDir:     opts.OutDir,
	}, nil
}

// unwrapDataKey recovers the Data Key from the Take-Out's envelope.
//
// Failure modes are reported distinctly enough to be actionable — wrong key,
// damaged file, format from the future — but never reveal key material and never
// say *why* the AEAD rejected the input.
func unwrapDataKey(dir string, m takeout.Manifest, recoveryKey []byte) ([]byte, error) {
	if len(recoveryKey) == 0 {
		return nil, fmt.Errorf("takeout: recovery key is required")
	}
	if m.EnvelopeRef == "" && m.Envelope == nil {
		return nil, takeout.ErrNoEnvelope
	}

	blob, err := os.ReadFile(m.EnvelopePath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, takeout.ErrNoEnvelope
		}
		return nil, fmt.Errorf("takeout: read key envelope: %w", err)
	}

	info, err := keys.Inspect(blob)
	if err != nil {
		// A version this build does not know is the one case worth naming: the
		// user needs a newer decrypt tool, not a different key.
		if errors.Is(err, keys.ErrBadEnvelope) && isFutureVersion(blob) {
			return nil, ErrUnsupportedEnvelope
		}
		return nil, fmt.Errorf("%w: key envelope is unreadable", takeout.ErrCorrupt)
	}
	if info.Kind != keys.WrapRK {
		return nil, fmt.Errorf("%w: stored envelope is not a recovery-key envelope", takeout.ErrCorrupt)
	}

	dk, err := keys.UnwrapRK(keys.WrappedDK{
		Version: info.Version,
		Kind:    keys.WrapRK,
		Blob:    blob,
	}, recoveryKey)
	if err != nil {
		return nil, ErrWrongRecoveryKey
	}
	return dk, nil
}

// isFutureVersion reports whether a blob looks like a well-formed envelope whose
// version is simply newer than this build's. It inspects the fixed header only.
func isFutureVersion(blob []byte) bool {
	if len(blob) < 6 || string(blob[:5]) != "OCBKE" {
		return false
	}
	return int(blob[5]) > keys.EnvelopeVersion
}

// openRepo builds an engine over the Take-Out's repository directory.
func openRepo(dir string, m takeout.Manifest, dk []byte, workDir string) (*snapshot.KopiaEngine, snapshot.Repo, error) {
	repoDir := m.RepoPath(dir)
	if _, err := os.Stat(repoDir); err != nil {
		return nil, snapshot.Repo{}, fmt.Errorf("%w: repository directory is missing", takeout.ErrCorrupt)
	}

	engine, err := snapshot.NewEngine(snapshot.DirOpener{Dir: repoDir}, snapshot.EngineOptions{WorkDir: workDir})
	if err != nil {
		return nil, snapshot.Repo{}, err
	}
	return engine, snapshot.Repo{
		Space: snapshot.SpaceRef{SpaceID: m.SpaceID},
		DK:    dk,
	}, nil
}

// selectSnapshot picks the requested snapshot, or the newest one.
func selectSnapshot(infos []snapshot.Info, id string) (snapshot.Info, error) {
	if len(infos) == 0 {
		return snapshot.Info{}, fmt.Errorf("takeout: the take-out contains no snapshots")
	}
	if id == "" {
		// List returns newest first.
		return infos[0], nil
	}
	for _, info := range infos {
		if string(info.ID) == id {
			return info, nil
		}
	}
	return snapshot.Info{}, fmt.Errorf("%w: %s", snapshot.ErrSnapshotNotFound, id)
}
