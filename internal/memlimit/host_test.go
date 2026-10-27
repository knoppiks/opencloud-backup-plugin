package memlimit

import (
	"errors"
	"testing"
	"testing/fstest"

	"github.com/knoppiks/opencloud-backup-plugin/internal/memfs"
)

func file(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }

func TestCgroupLimit(t *testing.T) {
	cases := map[string]struct {
		fs         fstest.MapFS
		want       int64
		wantErr    error
		wantAnyErr bool
	}{
		// Docker and Kubernetes on cgroup v2 give the container its own
		// namespace: it sees itself as the root.
		"own namespace": {fs: fstest.MapFS{
			"proc/self/cgroup":         file("0::/\n"),
			"sys/fs/cgroup/memory.max": file("536870912\n"),
		}, want: 512 * mib},
		"own namespace, no limit": {fs: fstest.MapFS{
			"proc/self/cgroup":         file("0::/\n"),
			"sys/fs/cgroup/memory.max": file("max\n"),
		}, want: 0},
		// Without a namespace the full path shows, and a tighter limit
		// higher up (a Kubernetes pod) applies.
		"host namespace, parent tighter": {fs: fstest.MapFS{
			"proc/self/cgroup":                           file("0::/kubepods/pod1/ctr\n"),
			"sys/fs/cgroup/kubepods/pod1/ctr/memory.max": file("1073741824\n"),
			"sys/fs/cgroup/kubepods/pod1/memory.max":     file("536870912\n"),
			"sys/fs/cgroup/kubepods/memory.max":          file("max\n"),
		}, want: 512 * mib},
		"host namespace, own tighter": {fs: fstest.MapFS{
			"proc/self/cgroup": file("0::/system.slice/docker-x.scope\n"),
			"sys/fs/cgroup/system.slice/docker-x.scope/memory.max": file("268435456\n"),
			"sys/fs/cgroup/system.slice/memory.max":                file("max\n"),
		}, want: 256 * mib},
		"hybrid v1 lines are ignored": {fs: fstest.MapFS{
			"proc/self/cgroup":         file("12:memory:/docker/x\n0::/\n"),
			"sys/fs/cgroup/memory.max": file("536870912\n"),
		}, want: 512 * mib},
		"cgroup v1 only": {fs: fstest.MapFS{
			"proc/self/cgroup": file("12:memory:/docker/x\n11:cpu:/docker/x\n"),
		}, wantErr: ErrNoCgroupV2},
		"no proc (not Linux)": {fs: fstest.MapFS{}, wantErr: ErrNoCgroupV2},
		"garbage": {fs: fstest.MapFS{
			"proc/self/cgroup":         file("0::/\n"),
			"sys/fs/cgroup/memory.max": file("lots\n"),
		}, wantAnyErr: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := CgroupLimit(c.fs)
			switch {
			case c.wantAnyErr:
				if err == nil {
					t.Fatalf("CgroupLimit = %d, want an error", got)
				}
			case c.wantErr != nil:
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("CgroupLimit = %d, %v; want %v", got, err, c.wantErr)
				}
			case err != nil || got != c.want:
				t.Fatalf("CgroupLimit = %d, %v; want %d", got, err, c.want)
			}
		})
	}
}

func TestMemoryFSSize(t *testing.T) {
	filesystems := map[string]memfs.Filesystem{
		"/work":     {Memory: true, Size: 256 * mib, Device: 1},
		"/work/sub": {Memory: true, Size: 256 * mib, Device: 1},
		"/tmp":      {Memory: true, Size: 16 * mib, Device: 2},
		"/data":     {Memory: false, Size: 1 << 40, Device: 3},
		"/ramfs":    {Memory: true, Size: 0, Device: 4},
	}
	inspect := func(dir string) (memfs.Filesystem, error) {
		fs, ok := filesystems[dir]
		if !ok {
			return memfs.Filesystem{}, errors.New("no such directory")
		}
		return fs, nil
	}

	cases := map[string]struct {
		dirs    []string
		want    int64
		wantErr bool
	}{
		"work and tmp":                {dirs: []string{"/work", "/tmp"}, want: 272 * mib},
		"one filesystem counted once": {dirs: []string{"/work", "/work/sub"}, want: 256 * mib},
		"disk costs no memory":        {dirs: []string{"/data", "/tmp"}, want: 16 * mib},
		"unbounded":                   {dirs: []string{"/ramfs"}, wantErr: true},
		"missing":                     {dirs: []string{"/nope"}, wantErr: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := MemoryFSSize(inspect, c.dirs)
			if c.wantErr {
				if err == nil {
					t.Fatalf("MemoryFSSize = %d, want an error", got)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("MemoryFSSize = %d, %v; want %d", got, err, c.want)
			}
		})
	}
	if _, err := MemoryFSSize(inspect, []string{"/ramfs"}); !errors.Is(err, ErrUnboundedMemoryFS) {
		t.Fatalf("ramfs: %v", err)
	}
}
