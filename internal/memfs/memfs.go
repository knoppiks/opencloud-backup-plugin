// Package memfs answers what the memory limit (internal/memlimit) needs to
// know about the filesystems the service writes to: whether one lives in
// memory, and how much of the container's memory it can take. tmpfs pages
// are charged to the same cgroup as the heap.
//
// internal/snapshot has its own memory-backed check on purpose: it is linked
// into the offline decrypt tool, whose package set is kept minimal
// (cmd/decrypt/deps_test.go).
package memfs

import "errors"

// ErrUnsupported is returned where the filesystem cannot be inspected. The
// containerised deployment is Linux; elsewhere (developer machines, the
// offline CLIs) callers treat it as "not applicable".
var ErrUnsupported = errors.New("memfs: cannot inspect filesystems on this platform")

// Filesystem is what statfs(2) says about the filesystem a directory is on.
type Filesystem struct {
	// Memory reports a memory-backed filesystem (tmpfs or ramfs).
	Memory bool
	// Size is the filesystem's capacity in bytes. A memory-backed one with no
	// size limit (ramfs) reports 0.
	Size int64
	// Device identifies the filesystem, so two directories on the same one
	// are counted once.
	Device uint64
}
