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
//
// The engine is implemented in Phase 4. This file defines the boundary interface
// only.
package snapshot

import (
	"context"
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

// Engine is the repo-per-Space lifecycle boundary. The DK (repo password) is
// passed per call and never retained or logged.
type Engine interface {
	// Snapshot creates a snapshot of srcDir into the space's repo and returns
	// the new snapshot's info.
	Snapshot(ctx context.Context, ref SpaceRef, dk []byte, srcDir string) (Info, error)
	// RestoreAll materialises the given snapshot fully into outDir.
	RestoreAll(ctx context.Context, ref SpaceRef, dk []byte, id SnapshotID, outDir string) error
	// RestoreFile materialises a single file/subtree (space-relative path) into
	// outDir. Backlog for v1 UI, but the engine boundary supports it
	// (decisions.md #3).
	RestoreFile(ctx context.Context, ref SpaceRef, dk []byte, id SnapshotID, relPath, outDir string) error
	// Prune deletes snapshots older than now-window (time-based keep-within) and
	// runs maintenance GC. Runs as a separate job (decisions.md #9 Tier 1).
	Prune(ctx context.Context, ref SpaceRef, dk []byte, window time.Duration) error
	// List returns the snapshots currently in the space's repo, newest first.
	List(ctx context.Context, ref SpaceRef, dk []byte) ([]Info, error)
}
