// Package snapshot wraps kopia (as a library) into a repo-per-Space lifecycle:
// snapshot, restore (full and single-file), and time-based prune.
//
// Design constraints from Phase 0 / decisions.md:
//   - kopia owns encryption and dedup; we never hand-roll either (decisions.md #5).
//   - The repo password is the Space's Data Key (DK); never log it
//     (phase-0-findings.md Spike 2).
//   - Retention is time-based (keep-within). kopia's own retention is count-based
//     only, so keep-within is implemented above kopia: list snapshots, delete
//     manifests older than the window, then run full maintenance GC
//     (decisions.md #10; phase-0-findings.md Spike 2, "Pruning / retention").
//   - Prune runs as a separate job from backup (decisions.md #9 Tier 1).
package snapshot

import (
	"context"
	"io"
	"time"
)

// SpaceRef deterministically identifies the source Space for a snapshot. It maps
// to kopia's snapshot.SourceInfo{Host, UserName, Path} (phase-0-findings Spike 2).
type SpaceRef struct {
	// SpaceID is the CS3 space ID; the repo and snapshot chain are per-Space
	// (decisions.md #6).
	SpaceID string
}

// SnapshotID identifies one persisted snapshot manifest within a repo.
type SnapshotID string

// Info describes one snapshot in a repo.
type Info struct {
	ID        SnapshotID
	StartTime time.Time
	// FileCount and TotalBytes are logical (pre-dedup) counts.
	FileCount  int64
	TotalBytes int64
}

// Location addresses the S3 target holding a Space's repo. Credentials are
// plaintext and therefore exist only in worker memory for the duration of a run
// (decisions.md #14); they are never logged and never serialized.
type Location struct {
	// Endpoint is host:port with no scheme (kopia's S3 driver requirement,
	// phase-0-findings.md Spike 2).
	Endpoint string
	Region   string
	Bucket   string
	// Prefix namespaces this deployment inside the bucket. The per-Space repo
	// lives beneath it at "<Prefix>spaces/<space-id>/" (see RepoPrefix).
	Prefix string
	// AccessKeyID / SecretAccessKey are the target's S3 credentials. Never
	// logged, never returned by any API.
	AccessKeyID     string
	SecretAccessKey string
	// DisableTLS talks plain HTTP (in-cluster Garage).
	DisableTLS bool
}

// Redacted returns a copy safe to include in logs: credentials removed.
func (l Location) Redacted() Location {
	l.AccessKeyID = ""
	l.SecretAccessKey = ""
	return l
}

// Repo identifies one Space's kopia repository: where it lives, which Space it
// belongs to, and the key that opens it.
//
// DK is the repo password. It must never be logged, serialized, or included in
// an error message; the caller owns the buffer and zeroizes it after the run.
type Repo struct {
	Location Location
	Space    SpaceRef
	DK       []byte
}

// Node is one entry in a Source tree. It carries exactly the metadata that is
// in backup scope: name, structure, size, mtime (decisions.md #4).
type Node struct {
	// Name is the base name of the entry within its parent directory.
	Name string
	// IsDir reports whether the entry is a directory.
	IsDir bool
	// Size is the file size in bytes (0 for directories).
	Size int64
	// ModTime is the modification time; preserved through snapshot and restore.
	ModTime time.Time
}

// Source is the tree the engine snapshots, kept deliberately free of kopia
// types so the CS3-backed implementation (Phase 4 Option B: stream on demand,
// no staging) and any future staging implementation are swappable, and so tests
// can supply a trivial fake.
//
// Paths are slash-separated and relative to the source root, with no leading
// "./" or "/". The empty path denotes the root directory.
type Source interface {
	// Name is the root directory name recorded in the snapshot.
	Name() string
	// List returns the direct children of the directory at dir.
	List(ctx context.Context, dir string) ([]Node, error)
	// Open streams the file at path starting at offset bytes. The caller closes
	// the returned reader.
	Open(ctx context.Context, path string, offset int64) (io.ReadCloser, error)
}

// Engine is the repo-per-Space lifecycle boundary. The DK travels inside Repo
// and is never retained or logged.
type Engine interface {
	// Snapshot creates a snapshot of src in the space's repo, creating the repo
	// on first use, and returns the new snapshot's info.
	Snapshot(ctx context.Context, repo Repo, src Source) (Info, error)
	// RestoreAll materialises the given snapshot fully into outDir.
	RestoreAll(ctx context.Context, repo Repo, id SnapshotID, outDir string) error
	// RestoreFile materialises a single file/subtree (space-relative path) into
	// outDir. Backlog for v1 UI, but the engine boundary supports it
	// (decisions.md #3).
	RestoreFile(ctx context.Context, repo Repo, id SnapshotID, relPath, outDir string) error
	// Prune deletes snapshots older than now-window (time-based keep-within) and
	// runs maintenance GC. Runs as a separate job (decisions.md #9 Tier 1).
	Prune(ctx context.Context, repo Repo, window time.Duration) error
	// List returns the snapshots currently in the space's repo, newest first.
	List(ctx context.Context, repo Repo) ([]Info, error)
}
