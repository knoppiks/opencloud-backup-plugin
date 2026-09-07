package snapshot

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// memSource is an in-memory Source used across this package's tests. It records
// how often files are opened so tests can assert on dedup / cache behaviour.
type memSource struct {
	name string

	mu    sync.Mutex
	files map[string]memFile
	// opens counts Open calls per path.
	opens map[string]int
	// openErr, when set for a path, makes Open fail (failure-injection tests).
	openErr map[string]error
	// listErr, when set for a dir, makes List fail.
	listErr map[string]error
}

type memFile struct {
	data    []byte
	modTime time.Time
	// dirModTime applies to the synthesised parent directories.
}

func newMemSource(name string) *memSource {
	return &memSource{
		name:    name,
		files:   map[string]memFile{},
		opens:   map[string]int{},
		openErr: map[string]error{},
		listErr: map[string]error{},
	}
}

// put adds or replaces a file at a slash-separated, root-relative path.
func (m *memSource) put(p string, data []byte, modTime time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[p] = memFile{data: data, modTime: modTime}
}

func (m *memSource) remove(p string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, p)
}

func (m *memSource) openCount(p string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.opens[p]
}

func (m *memSource) failOpen(p string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.openErr[p] = err
}

func (m *memSource) failList(dir string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listErr[dir] = err
}

func (m *memSource) Name() string { return m.name }

// List synthesises directory structure from the stored file paths.
func (m *memSource) List(_ context.Context, dir string) ([]Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.listErr[dir]; err != nil {
		return nil, err
	}

	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}

	files := map[string]Node{}
	dirs := map[string]struct{}{}
	for p, f := range m.files {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := strings.TrimPrefix(p, prefix)
		if rest == "" {
			continue
		}
		if i := strings.Index(rest, "/"); i >= 0 {
			dirs[rest[:i]] = struct{}{}
			continue
		}
		files[rest] = Node{
			Name:    rest,
			Size:    int64(len(f.data)),
			ModTime: f.modTime,
		}
	}

	out := make([]Node, 0, len(files)+len(dirs))
	for d := range dirs {
		// Directory mtimes are constant so they never trigger a cache miss.
		out = append(out, Node{Name: d, IsDir: true, ModTime: fixedDirTime})
	}
	for _, n := range files {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *memSource) Open(_ context.Context, p string, offset int64) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.openErr[p]; err != nil {
		return nil, err
	}
	f, ok := m.files[p]
	if !ok {
		return nil, fmt.Errorf("memSource: no file %q", p)
	}
	if offset > int64(len(f.data)) {
		return nil, fmt.Errorf("memSource: offset %d past end of %q", offset, p)
	}
	m.opens[p]++
	return io.NopCloser(bytes.NewReader(f.data[offset:])), nil
}

// slowSource makes every file read take a fixed time, so an upload can be
// driven past kopia's checkpoint interval without a real multi-gigabyte Space.
type slowSource struct {
	*memSource
	delay time.Duration
}

func (s slowSource) Open(ctx context.Context, p string, offset int64) (io.ReadCloser, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.memSource.Open(ctx, p, offset)
}

// fixedDirTime keeps synthesised directory mtimes stable across runs.
var fixedDirTime = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// join is a small helper mirroring the Source path convention.
func join(parts ...string) string { return path.Join(parts...) }
