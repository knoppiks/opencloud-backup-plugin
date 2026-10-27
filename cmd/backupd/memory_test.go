package main

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/knoppiks/opencloud-backup-plugin/internal/memlimit"
)

// memoryHost is a memlimit.Host with fixed answers that records what it was
// asked and what was set.
type memoryHost struct {
	env       string
	container int64
	limitErr  error
	memoryFS  int64
	asked     []string
	set       int64
}

func (m *memoryHost) host() memlimit.Host {
	return memlimit.Host{
		Getenv:         func(string) string { return m.env },
		ContainerLimit: func() (int64, error) { return m.container, m.limitErr },
		MemoryFS: func(dirs []string) (int64, error) {
			m.asked = dirs
			return m.memoryFS, nil
		},
		SetMemoryLimit: func(n int64) int64 {
			if n >= 0 {
				m.set = n
			}
			return m.set
		},
	}
}

func TestConfigureMemoryLimit(t *testing.T) {
	const mib = 1 << 20
	cases := map[string]struct {
		host    memoryHost
		wantLog string
		wantSet int64
	}{
		"derived": {
			host:    memoryHost{container: 512 * mib, memoryFS: 272 * mib},
			wantLog: `"msg":"memory limit derived from the container's","limit_bytes":226492416`,
			wantSet: 216 * mib,
		},
		"from GOMEMLIMIT": {
			host:    memoryHost{env: "300MiB", set: 300 * mib},
			wantLog: `"msg":"memory limit set by GOMEMLIMIT","limit_bytes":314572800`,
			wantSet: 300 * mib,
		},
		"no container limit": {
			host:    memoryHost{},
			wantLog: `"level":"INFO","msg":"no container memory limit`,
		},
		"no cgroup v2": {
			host:    memoryHost{limitErr: memlimit.ErrNoCgroupV2},
			wantLog: `"level":"INFO","msg":"container memory limit unknown (no cgroup v2); set GOMEMLIMIT`,
		},
		"tmpfs too large": {
			host:    memoryHost{container: 512 * mib, memoryFS: 8 << 30},
			wantLog: `"level":"WARN","msg":"memory limit not set; give the memory-backed directories a size`,
		},
		"cannot read": {
			host:    memoryHost{limitErr: errors.New("permission denied")},
			wantLog: `"level":"WARN","msg":"memory limit not set`,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			h := c.host
			configureMemoryLimit(h.host(), workEnv{WorkDir: "/work"}, slog.New(slog.NewJSONHandler(&buf, nil)))
			if !strings.Contains(buf.String(), c.wantLog) {
				t.Fatalf("log %s\nwant %s", buf.String(), c.wantLog)
			}
			if h.set != c.wantSet {
				t.Fatalf("limit %d, want %d", h.set, c.wantSet)
			}
		})
	}
}

// Both directories the service writes to count: the work directory and the
// temp directory kopia and the runtime use.
func TestConfigureMemoryLimit_AsksAboutWorkAndTempDirs(t *testing.T) {
	h := memoryHost{container: 1 << 30}
	configureMemoryLimit(h.host(), workEnv{WorkDir: "/work"}, quietLogger())
	if len(h.asked) != 2 || h.asked[0] != "/work" || h.asked[1] != os.TempDir() {
		t.Fatalf("asked about %v", h.asked)
	}

	h = memoryHost{container: 1 << 30}
	configureMemoryLimit(h.host(), workEnv{}, quietLogger())
	if len(h.asked) != 2 || h.asked[0] != os.TempDir() {
		t.Fatalf("unset work dir: asked about %v", h.asked)
	}
}
