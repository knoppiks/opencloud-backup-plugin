package main

// Deployment preconditions checked at startup.
//
// The service has a small number of requirements that are cheap to state and
// expensive to discover later: exactly one instance, durable state, a work
// directory whose contents die with the pod, and TLS in front of a listener that
// carries a Data Key once per Space. Each of them used to be a sentence in a
// document while the shipped manifest quietly violated it (review-2026-09.md
// F5/F6). They are checked here instead.

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"opencloud-backup-plugin/pkg/snapshot"
)

const (
	// workDirVar is the parent directory of the per-run kopia config and cache.
	workDirVar = "BACKUP_WORK_DIR"
	// workDirAllowDiskVar lets an operator accept a disk-backed work directory.
	workDirAllowDiskVar = "BACKUP_WORK_DIR_ALLOW_DISK"
)

// mountBasePath serves h under basePath, keeping the health probes at the root.
//
// The probes are hit directly on the pod by the kubelet, which knows nothing
// about the ingress that adds the prefix; moving them would turn a path prefix
// into a failing readiness check.
func mountBasePath(h http.Handler, basePath string) http.Handler {
	if basePath == "" {
		return h
	}
	mux := http.NewServeMux()
	for _, probe := range probePaths {
		mux.Handle(probe, h)
	}
	mux.Handle(basePath+"/", http.StripPrefix(basePath, h))
	return mux
}

// resolveWorkDir validates the per-run work directory and clears what a previous
// process left in it, returning the configured value for EngineOptions.
//
// A run's work directory holds kopia's cache — repository content, ciphertext,
// but still the family's data — and, before R5, the target's credentials.
// Credentials no longer go there at all (pkg/snapshot/handle.go); a memory-backed
// filesystem takes care of the rest by making "left behind after a crash"
// impossible rather than unlikely.
func resolveWorkDir(cfg workEnv, logger *slog.Logger) (string, error) {
	dir := cfg.WorkDir
	resolved := snapshot.WorkDirOrTemp(dir)

	if removed, err := snapshot.SweepWorkDir(dir); err != nil {
		// Not fatal: the directories are junk, and failing to remove junk is no
		// reason to refuse to back anything up.
		logger.Warn("could not remove run directories left by a previous process", "err", err)
	} else if removed > 0 {
		logger.Warn("removed run directories left by a previous process", "directories", removed)
	}

	memory, err := snapshot.MemoryBacked(dir)
	switch {
	case errors.Is(err, snapshot.ErrWorkDirFilesystemUnknown):
		logger.Warn("cannot determine the work directory's filesystem on this platform", "dir", resolved)
	case err != nil:
		return "", fmt.Errorf("%s: %w", workDirVar, err)
	case memory:
		logger.Info("work directory is memory-backed", "dir", resolved)
	case cfg.WorkDirAllowDisk:
		logger.Warn("work directory is on disk; kopia's per-run cache can outlive a crash there. "+
			"Accepted because "+workDirAllowDiskVar+" is set", "dir", resolved)
	default:
		return "", fmt.Errorf(
			"%s (%s) is not on a memory-backed filesystem: mount it as an emptyDir with "+
				"medium Memory, or set %s=true to accept that kopia's per-run cache can "+
				"outlive a crash there",
			workDirVar, resolved, workDirAllowDiskVar)
	}
	return dir, nil
}

// chain composes cleanup functions, running them in the order given.
func chain(fns ...func()) func() {
	return func() {
		for _, fn := range fns {
			if fn != nil {
				fn()
			}
		}
	}
}
