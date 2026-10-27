package memlimit

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/knoppiks/opencloud-backup-plugin/internal/memfs"
)

// Production is the running process.
func Production() Host {
	root := os.DirFS("/")
	return Host{
		Getenv:         os.Getenv,
		ContainerLimit: func() (int64, error) { return CgroupLimit(root) },
		MemoryFS:       func(dirs []string) (int64, error) { return MemoryFSSize(memfs.Inspect, dirs) },
		SetMemoryLimit: debug.SetMemoryLimit,
	}
}

// CgroupLimit reads the memory limit of this process's cgroup (v2) from
// root, the filesystem root. Limits on the cgroups above it count too: the
// tightest one visible applies. It returns 0 when none is set.
//
// Inside a container with its own cgroup namespace, /proc/self/cgroup names
// "/" and the container's cgroup is the root of /sys/fs/cgroup; without one
// it names the full path. Both are handled the same way.
func CgroupLimit(root fs.FS) (int64, error) {
	own, err := ownCgroup(root)
	if err != nil {
		return 0, err
	}
	var limit int64
	for dir := own; ; dir = path.Dir(dir) {
		value, err := fs.ReadFile(root, path.Join("sys/fs/cgroup", dir, "memory.max"))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// The root cgroup has no memory.max; nor does a level the
			// namespace hides.
		case err != nil:
			return 0, fmt.Errorf("memlimit: read cgroup limit: %w", err)
		default:
			level, err := parseMemoryMax(value)
			if err != nil {
				return 0, err
			}
			if level > 0 && (limit == 0 || level < limit) {
				limit = level
			}
		}
		if dir == "/" {
			return limit, nil
		}
	}
}

// ownCgroup is the cgroup v2 path in /proc/self/cgroup ("0::<path>").
func ownCgroup(root fs.FS) (string, error) {
	data, err := fs.ReadFile(root, "proc/self/cgroup")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", ErrNoCgroupV2
		}
		return "", fmt.Errorf("memlimit: read own cgroup: %w", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		if p, ok := strings.CutPrefix(scanner.Text(), "0::"); ok && strings.HasPrefix(p, "/") {
			return path.Clean(p), nil
		}
	}
	return "", ErrNoCgroupV2
}

// parseMemoryMax reads memory.max: a byte count, or "max" for none (0).
func parseMemoryMax(value []byte) (int64, error) {
	s := strings.TrimSpace(string(value))
	if s == "max" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("memlimit: unexpected memory.max %q", s)
	}
	return n, nil
}

// MemoryFSSize adds up the capacity of the distinct memory-backed
// filesystems among dirs; disk-backed ones cost no memory. inspect is
// memfs.Inspect in production.
func MemoryFSSize(inspect func(string) (memfs.Filesystem, error), dirs []string) (int64, error) {
	seen := map[uint64]bool{}
	var total int64
	for _, dir := range dirs {
		fsys, err := inspect(dir)
		if err != nil {
			return 0, fmt.Errorf("memlimit: %w", err)
		}
		if !fsys.Memory || seen[fsys.Device] {
			continue
		}
		seen[fsys.Device] = true
		if fsys.Size <= 0 {
			return 0, fmt.Errorf("%w: %s", ErrUnboundedMemoryFS, dir)
		}
		total = saturatingAdd(total, fsys.Size)
	}
	return total, nil
}

func saturatingAdd(a, b int64) int64 {
	if a > (1<<63-1)-b {
		return 1<<63 - 1
	}
	return a + b
}
