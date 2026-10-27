//go:build linux

package memfs

import (
	"fmt"
	"math"

	"golang.org/x/sys/unix"
)

// Filesystem type magic numbers for the two memory-backed filesystems Linux
// offers. tmpfs is what an emptyDir with `medium: Memory` and a compose
// `tmpfs:` entry mount. Same values as internal/snapshot/workdir_linux.go.
const (
	tmpfsMagic = 0x01021994
	ramfsMagic = 0x858458f6
)

// Inspect answers from statfs(2) and stat(2).
func Inspect(dir string) (Filesystem, error) {
	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err != nil {
		return Filesystem{}, fmt.Errorf("memfs: inspect %q: %w", dir, err)
	}
	var st unix.Stat_t
	if err := unix.Stat(dir, &st); err != nil {
		return Filesystem{}, fmt.Errorf("memfs: inspect %q: %w", dir, err)
	}
	return Filesystem{
		Memory: isMemoryType(int64(fs.Type)),
		Size:   capacity(fs.Blocks, int64(fs.Bsize)),
		Device: uint64(st.Dev), //nolint:unconvert // Dev is uint32 on some architectures
	}, nil
}

func isMemoryType(magic int64) bool {
	return magic == tmpfsMagic || magic == ramfsMagic
}

// capacity is blocks*blockSize, saturating instead of overflowing: a
// filesystem too large to count is larger than any container limit.
func capacity(blocks uint64, blockSize int64) int64 {
	if blockSize <= 0 {
		return 0
	}
	if blocks > uint64(math.MaxInt64/blockSize) { //nolint:gosec // blockSize > 0, checked above
		return math.MaxInt64
	}
	return int64(blocks) * blockSize //nolint:gosec // blocks <= MaxInt64/blockSize, checked above
}
