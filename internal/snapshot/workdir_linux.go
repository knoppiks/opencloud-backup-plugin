//go:build linux

package snapshot

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Filesystem type magic numbers for the two memory-backed filesystems Linux
// offers. tmpfs is what an emptyDir with `medium: Memory` mounts.
const (
	tmpfsMagic = 0x01021994
	ramfsMagic = 0x858458f6
)

// memoryBackedFilesystem answers from statfs(2).
func memoryBackedFilesystem(dir string) (bool, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return false, fmt.Errorf("snapshot: inspect work directory %q: %w", dir, err)
	}
	switch int64(st.Type) {
	case tmpfsMagic, ramfsMagic:
		return true, nil
	default:
		return false, nil
	}
}
