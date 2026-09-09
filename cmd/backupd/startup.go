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
	"os"
	"sort"
	"strings"

	"opencloud-backup-plugin/pkg/snapshot"
)

const (
	// workDirVar is the parent directory of the per-run kopia config and cache.
	workDirVar = "BACKUP_WORK_DIR"
	// workDirAllowDiskVar lets an operator accept a disk-backed work directory.
	workDirAllowDiskVar = "BACKUP_WORK_DIR_ALLOW_DISK"
	// stateBackendVar selects the state backend; only "memory" is special.
	stateBackendVar = "STATE_BACKEND"
	// stateBackendMemory is the explicit opt-in to throwaway state.
	stateBackendMemory = "memory"
	// tlsCertVar / tlsKeyVar make the service terminate TLS itself.
	tlsCertVar = "TLS_CERT_FILE"
	tlsKeyVar  = "TLS_KEY_FILE"
)

// resolveWorkDir validates the per-run work directory and clears what a previous
// process left in it, returning the configured value for EngineOptions.
//
// A run's work directory holds kopia's cache — repository content, ciphertext,
// but still the family's data — and, before R5, the target's credentials.
// Credentials no longer go there at all (pkg/snapshot/handle.go); a memory-backed
// filesystem takes care of the rest by making "left behind after a crash"
// impossible rather than unlikely.
func resolveWorkDir(logger *slog.Logger) (string, error) {
	dir := os.Getenv(workDirVar)
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
	case envBool(workDirAllowDiskVar):
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

// placeholderMarker is what the shipped manifests put where a deployer must fill
// something in.
const placeholderMarker = "REPLACE_ME"

// configPrefixes are the environment variables this service reads. The list is
// by prefix so it does not have to track every variable, and it is scoped so a
// placeholder belonging to some other component of the deployment is not this
// service's business to refuse.
var configPrefixes = []string{
	"ADMIN_", "BACKUP_", "BACKUPD_", "BOOTSTRAP_", "CS3_", "JOB_", "NOTIFY_",
	"OC_", "OIDC_", "PRUNE_", "SCHEDULE", "SMTP_", "SRW_KEY", "STATE_",
	"TLS_", "TW_KEY",
}

// checkPlaceholders refuses to start on a manifest that was deployed unedited.
//
// The placeholders are not all equal: an unreplaced wrapping key fails loudly on
// its own (it is not base64), while an unreplaced OIDC audience or state Space id
// used to start perfectly well and then reject every token, or write state to a
// Space that does not exist. "Comes up and does not work" is the worst of the
// available outcomes, because it looks like a bug in the service rather than an
// unfinished deployment.
func checkPlaceholders(environ []string) error {
	names := unreplacedPlaceholders(environ)
	if len(names) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%s still holds the manifest's placeholder value: fill it in before deploying "+
			"(see deploy/ and the README preconditions)", strings.Join(names, ", "))
}

// unreplacedPlaceholders returns the names of this service's configuration
// variables whose value still contains the placeholder marker.
func unreplacedPlaceholders(environ []string) []string {
	var names []string
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.Contains(value, placeholderMarker) {
			continue
		}
		for _, prefix := range configPrefixes {
			if strings.HasPrefix(name, prefix) {
				names = append(names, name)
				break
			}
		}
	}
	sort.Strings(names)
	return names
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

// memoryStateRequested reports whether the operator explicitly asked for
// throwaway state.
func memoryStateRequested() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(stateBackendVar)), stateBackendMemory)
}

// tlsFiles returns the certificate and key the listener should use, if any, and
// refuses a half-configured pair.
//
// The listener is plain HTTP by default and must sit behind a TLS-terminating
// ingress on the same origin as OpenCloud: a Space's Data Key crosses it once,
// at key setup (decisions.md, trust & key model). A deployment with nothing in
// front of it sets these two variables instead.
func tlsFiles() (certFile, keyFile string, err error) {
	certFile = strings.TrimSpace(os.Getenv(tlsCertVar))
	keyFile = strings.TrimSpace(os.Getenv(tlsKeyVar))
	switch {
	case certFile == "" && keyFile == "":
		return "", "", nil
	case certFile == "" || keyFile == "":
		return "", "", fmt.Errorf("%s and %s must be set together", tlsCertVar, tlsKeyVar)
	default:
		return certFile, keyFile, nil
	}
}
