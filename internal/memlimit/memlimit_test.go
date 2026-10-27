package memlimit

import (
	"errors"
	"testing"
)

const mib = 1 << 20

func TestSoftLimit(t *testing.T) {
	cases := map[string]struct {
		budget  Budget
		want    int64
		wantErr error
	}{
		// The shipped Kubernetes manifest: 512Mi, /work 256Mi, /tmp 16Mi.
		"kubernetes manifest":   {budget: Budget{Container: 512 * mib, MemoryFS: 272 * mib}, want: 216 * mib},
		"no memory-backed dirs": {budget: Budget{Container: 1000 * mib}, want: 900 * mib},
		"no container limit":    {budget: Budget{MemoryFS: 16 * mib}, wantErr: ErrNoContainerLimit},
		"tmpfs takes it all":    {budget: Budget{Container: 512 * mib, MemoryFS: 512 * mib}, wantErr: ErrTooSmall},
		"tmpfs larger":          {budget: Budget{Container: 512 * mib, MemoryFS: 8192 * mib}, wantErr: ErrTooSmall},
		"just under minimum":    {budget: Budget{Container: 71 * mib}, wantErr: ErrTooSmall},
		"just over minimum":     {budget: Budget{Container: 72 * mib}, want: 72 * mib / 10 * 9},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := c.budget.SoftLimit()
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("SoftLimit = %d, %v; want %v", got, err, c.wantErr)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("SoftLimit = %d, %v; want %d", got, err, c.want)
			}
		})
	}
}

// fakeHost records what Configure set.
type fakeHost struct {
	env       string
	container int64
	memoryFS  int64
	err       error
	set       []int64
	current   int64
	dirs      []string
}

func (f *fakeHost) host() Host {
	return Host{
		Getenv: func(name string) string {
			if name == EnvVar {
				return f.env
			}
			return ""
		},
		ContainerLimit: func() (int64, error) { return f.container, f.err },
		MemoryFS: func(dirs []string) (int64, error) {
			f.dirs = dirs
			return f.memoryFS, nil
		},
		SetMemoryLimit: func(n int64) int64 {
			if n >= 0 {
				f.set = append(f.set, n)
				f.current = n
			}
			return f.current
		},
	}
}

func TestConfigure_SetsTheDerivedLimit(t *testing.T) {
	f := &fakeHost{container: 512 * mib, memoryFS: 272 * mib}
	res, err := Configure(f.host(), []string{"/work", "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Limit != 216*mib || res.FromEnv || len(f.set) != 1 || f.set[0] != 216*mib {
		t.Fatalf("result %+v, set %v", res, f.set)
	}
	if res.Budget != (Budget{Container: 512 * mib, MemoryFS: 272 * mib}) {
		t.Fatalf("budget %+v", res.Budget)
	}
	if len(f.dirs) != 2 {
		t.Fatalf("MemoryFS asked about %v", f.dirs)
	}
}

// GOMEMLIMIT is the operator's word: the runtime applied it, nothing
// overrides it, and the cgroup is not even read.
func TestConfigure_GOMEMLIMITWins(t *testing.T) {
	f := &fakeHost{env: "300MiB", current: 300 * mib, err: errors.New("must not be read")}
	res, err := Configure(f.host(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.FromEnv || res.Limit != 300*mib || len(f.set) != 0 {
		t.Fatalf("result %+v, set %v", res, f.set)
	}
}

func TestConfigure_LeavesTheLimitUnsetWhenItCannotHelp(t *testing.T) {
	cases := map[string]struct {
		host    fakeHost
		wantErr error
	}{
		"no limit":      {host: fakeHost{}, wantErr: ErrNoContainerLimit},
		"no cgroup":     {host: fakeHost{err: ErrNoCgroupV2}, wantErr: ErrNoCgroupV2},
		"tmpfs too big": {host: fakeHost{container: 512 * mib, memoryFS: 4096 * mib}, wantErr: ErrTooSmall},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := c.host
			if _, err := Configure(f.host(), []string{"/tmp"}); !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if len(f.set) != 0 {
				t.Fatalf("a limit was set: %v", f.set)
			}
		})
	}
}
