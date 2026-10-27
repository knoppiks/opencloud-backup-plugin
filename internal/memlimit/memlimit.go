// Package memlimit sets the Go runtime's soft memory limit from the
// container's, so the garbage collector works harder before the kernel's
// out-of-memory killer acts.
//
// The container's limit is not all the heap's. The work directory and /tmp
// are memory-backed (tmpfs), and what is written there is charged to the same
// cgroup. So the soft limit is what is left once the memory-backed
// filesystems are full, less a margin for what the runtime holds outside the
// heap. GOMEMLIMIT, which the runtime reads itself, overrides all of this.
//
// kopia runs in this process, so its memory is covered too.
package memlimit

import (
	"errors"
	"fmt"
)

// EnvVar is the runtime's own variable. When it is set, the runtime has
// already applied it and nothing here changes it.
const EnvVar = "GOMEMLIMIT"

// MinLimit is the smallest soft limit worth setting. Below it the collector
// would run nearly continuously; better to leave the limit unset and say why.
const MinLimit int64 = 64 << 20

// The soft limit is this share of what the memory-backed filesystems leave,
// the rest being the runtime's own overhead (stacks, metadata, fragmentation).
const (
	shareNumerator   = 9
	shareDenominator = 10
)

// Sentinel outcomes that leave the limit unset.
var (
	// ErrNoContainerLimit: the container has no memory limit, so there is
	// nothing to stay under.
	ErrNoContainerLimit = errors.New("memlimit: the container has no memory limit")
	// ErrNoCgroupV2: the limit cannot be read (cgroup v1, or not in a
	// container). GOMEMLIMIT is the way to set one there.
	ErrNoCgroupV2 = errors.New("memlimit: no cgroup v2 memory controller found")
	// ErrTooSmall: the memory-backed filesystems leave too little for a
	// limit to help.
	ErrTooSmall = errors.New("memlimit: too little memory left for the heap")
	// ErrUnboundedMemoryFS: a memory-backed filesystem has no size limit, so
	// it can take all of the container's memory.
	ErrUnboundedMemoryFS = errors.New("memlimit: a memory-backed directory has no size limit")
)

// Budget is the container's memory and what the memory-backed directories
// can take of it, in bytes.
type Budget struct {
	Container int64
	MemoryFS  int64
}

// SoftLimit is the limit for b.
func (b Budget) SoftLimit() (int64, error) {
	if b.Container <= 0 {
		return 0, ErrNoContainerLimit
	}
	left := b.Container - b.MemoryFS
	limit := left / shareDenominator * shareNumerator
	if limit < MinLimit {
		return 0, fmt.Errorf("%w: %d bytes of %d remain after the memory-backed directories (%d bytes); the minimum is %d",
			ErrTooSmall, max(left, 0), b.Container, b.MemoryFS, MinLimit)
	}
	return limit, nil
}

// Host is what Configure reads and changes. Production wires the real
// process (Production); tests substitute each part.
type Host struct {
	// Getenv reads the environment.
	Getenv func(string) string
	// ContainerLimit is the container's memory limit in bytes, 0 for none.
	ContainerLimit func() (int64, error)
	// MemoryFS is how much the memory-backed directories among dirs can hold.
	MemoryFS func(dirs []string) (int64, error)
	// SetMemoryLimit is runtime/debug.SetMemoryLimit.
	SetMemoryLimit func(int64) int64
}

// Result is what Configure did.
type Result struct {
	// Limit is the soft limit in effect, in bytes.
	Limit int64
	// FromEnv reports that GOMEMLIMIT set it, not Configure.
	FromEnv bool
	// Budget is what the limit was derived from (zero when FromEnv).
	Budget Budget
}

// Configure sets the soft memory limit for a process writing to dirs. An
// error means nothing was set; the sentinel errors above say why.
func Configure(h Host, dirs []string) (Result, error) {
	if h.Getenv(EnvVar) != "" {
		return Result{Limit: h.SetMemoryLimit(-1), FromEnv: true}, nil
	}
	container, err := h.ContainerLimit()
	if err != nil {
		return Result{}, err
	}
	memoryFS, err := h.MemoryFS(dirs)
	if err != nil {
		return Result{}, err
	}
	budget := Budget{Container: container, MemoryFS: memoryFS}
	limit, err := budget.SoftLimit()
	if err != nil {
		return Result{Budget: budget}, err
	}
	h.SetMemoryLimit(limit)
	return Result{Limit: limit, Budget: budget}, nil
}
