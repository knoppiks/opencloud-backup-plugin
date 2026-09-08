package snapshot

// The per-run work directory: what may be written there, and where "there"
// should be.
//
// kopia insists on a local config file and cache directory per run (Phase-0
// Spike 2). Since R5 the config file holds no credentials (handle.go), but the
// cache still holds repository content — ciphertext, useless without the Data
// Key, and still a copy of the family's data on a disk this service does not
// need to write to. A memory-backed filesystem removes both the residue and the
// question, so the deployment mounts one and the service checks it rather than
// trusting the manifest to have stayed correct.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrWorkDirFilesystemUnknown is returned when the work directory's filesystem
// cannot be determined on this platform. It is not a failure of the directory —
// callers outside a container (the offline CLIs, developer machines) treat it as
// "not applicable".
var ErrWorkDirFilesystemUnknown = errors.New("snapshot: cannot determine the work directory's filesystem on this platform")

// WorkDirOrTemp resolves an EngineOptions.WorkDir value, applying the same
// default os.MkdirTemp would.
func WorkDirOrTemp(dir string) string {
	if dir == "" {
		return os.TempDir()
	}
	return dir
}

// MemoryBacked reports whether the work directory lives on a memory-backed
// filesystem (tmpfs or ramfs), so nothing written there can outlive the pod.
// It returns ErrWorkDirFilesystemUnknown on platforms that cannot answer.
func MemoryBacked(dir string) (bool, error) {
	return memoryBackedFilesystem(WorkDirOrTemp(dir))
}

// SweepWorkDir removes per-run kopia directories left behind by a process that
// did not get to clean up after itself, returning how many it removed.
//
// A run removes its own directory on the way out; anything found here belongs to
// an incarnation that was killed. Sweeping at startup keeps a crash-looping
// service from filling its work directory, and — on a disk-backed work dir —
// bounds how long the leftovers exist.
func SweepWorkDir(dir string) (int, error) {
	matches, err := filepath.Glob(filepath.Join(WorkDirOrTemp(dir), workDirPattern))
	if err != nil {
		return 0, fmt.Errorf("snapshot: scan work directory: %w", err)
	}

	removed := 0
	var failures error
	for _, m := range matches {
		if err := os.RemoveAll(m); err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		removed++
	}
	if failures != nil {
		return removed, fmt.Errorf("snapshot: remove leftover run directories: %w", failures)
	}
	return removed, nil
}
