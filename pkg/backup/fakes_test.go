package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/targets"
)

const (
	testSpaceID  = "storage-1$space-1"
	testTargetID = "target-1"
)

var testMTime = time.Date(2021, 3, 14, 15, 9, 26, 0, time.UTC)

// fakeReader is an in-memory cs3.SpaceReader over a flat path->content map.
type fakeReader struct {
	spaces []cs3.Space

	mu    sync.Mutex
	files map[string]fakeFile
	// listErr / openErr inject failures for a specific path.
	listErr map[string]error
	openErr map[string]error
	// opens counts OpenFile calls per path.
	opens map[string]int
	// listSpacesErr fails ListSpaces.
	listSpacesErr error
}

type fakeFile struct {
	data  []byte
	mtime time.Time
}

func newFakeReader(spaces ...cs3.Space) *fakeReader {
	return &fakeReader{
		spaces:  spaces,
		files:   map[string]fakeFile{},
		listErr: map[string]error{},
		openErr: map[string]error{},
		opens:   map[string]int{},
	}
}

func (f *fakeReader) put(p string, data []byte, mtime time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[p] = fakeFile{data: data, mtime: mtime}
}

func (f *fakeReader) openCount(p string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens[p]
}

// failOpen makes OpenFile fail for a path; pass nil to clear the failure.
func (f *fakeReader) failOpen(p string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openErr[p] = err
}

// space returns the single Space this reader serves.
func (f *fakeReader) space() cs3.Space { return f.spaces[0] }

func (f *fakeReader) ListSpaces(context.Context) ([]cs3.Space, error) {
	if f.listSpacesErr != nil {
		return nil, f.listSpacesErr
	}
	return f.spaces, nil
}

func (f *fakeReader) ListDir(_ context.Context, _ cs3.Space, dir string) ([]cs3.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.listErr[dir]; err != nil {
		return nil, err
	}

	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	var entries []cs3.Entry
	seenDirs := map[string]bool{}
	for p, file := range f.files {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := strings.TrimPrefix(p, prefix)
		if i := strings.Index(rest, "/"); i >= 0 {
			name := rest[:i]
			if seenDirs[name] {
				continue
			}
			seenDirs[name] = true
			entries = append(entries, cs3.Entry{
				Path: prefix + name, IsDir: true, MTimeUnix: fixedDirTime.Unix(),
			})
			continue
		}
		entries = append(entries, cs3.Entry{
			Path:      p,
			Size:      int64(len(file.data)),
			MTimeUnix: file.mtime.Unix(),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func (f *fakeReader) Walk(ctx context.Context, space cs3.Space, fn func(cs3.Entry) error) error {
	var walk func(string) error
	walk = func(dir string) error {
		entries, err := f.ListDir(ctx, space, dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := fn(e); err != nil {
				return err
			}
			if e.IsDir {
				if err := walk(e.Path); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk("")
}

func (f *fakeReader) OpenFile(_ context.Context, _ cs3.Space, p string, offset int64) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.openErr[p]; err != nil {
		return nil, err
	}
	file, ok := f.files[p]
	if !ok {
		return nil, fmt.Errorf("fakeReader: no file %q", p)
	}
	f.opens[p]++
	return io.NopCloser(bytes.NewReader(file.data[offset:])), nil
}

var fixedDirTime = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// fakeEngine records snapshot calls without touching kopia.
type fakeEngine struct {
	mu sync.Mutex

	calls []snapshot.Repo
	// sources records the Source handed to each call.
	sources []snapshot.Source
	info    snapshot.Info
	err     error
	// onSnapshot runs inside Snapshot, letting tests observe the live DK.
	onSnapshot func(snapshot.Repo)
}

var _ snapshot.Engine = (*fakeEngine)(nil)

func (e *fakeEngine) Snapshot(_ context.Context, r snapshot.Repo, src snapshot.Source) (snapshot.Info, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Copy the DK: the runner zeroizes its buffer after the call.
	captured := r
	captured.DK = append([]byte(nil), r.DK...)
	e.calls = append(e.calls, captured)
	e.sources = append(e.sources, src)
	if e.onSnapshot != nil {
		e.onSnapshot(r)
	}
	if e.err != nil {
		return snapshot.Info{}, e.err
	}
	return e.info, nil
}

func (e *fakeEngine) RestoreAll(context.Context, snapshot.Repo, snapshot.SnapshotID, string) error {
	return nil
}

func (e *fakeEngine) RestoreFile(context.Context, snapshot.Repo, snapshot.SnapshotID, string, string) error {
	return nil
}

func (e *fakeEngine) Prune(context.Context, snapshot.Repo, time.Duration) error { return nil }

func (e *fakeEngine) List(context.Context, snapshot.Repo) ([]snapshot.Info, error) { return nil, nil }

func (e *fakeEngine) lastCall(t *testing.T) snapshot.Repo {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.calls) == 0 {
		t.Fatal("engine was never called")
	}
	return e.calls[len(e.calls)-1]
}

func (e *fakeEngine) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

// seedTarget stores a target whose credentials are sealed with the given sealer.
func seedTarget(t *testing.T, store *targets.MemoryStore, sealer targets.CredSealer) targets.Target {
	t.Helper()
	wrapped, version, err := sealer.Seal(targets.PlainCreds{
		AccessKeyID:     "GK-test-access-key",
		SecretAccessKey: "test-secret-access-key-0000000000000000",
	})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	target, err := store.CreateTarget(context.Background(), targets.Target{
		ID:           testTargetID,
		Name:         "Buddy S3",
		Endpoint:     "garage:3900",
		Region:       "garage",
		Bucket:       "backups",
		Prefix:       "oc/",
		UsePathStyle: true,
		DisableTLS:   true,
		WrappedCreds: wrapped,
		Version:      version,
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	return target
}

// seedKeys generates a Data Key for the Space and stores its SRW envelope.
func seedKeys(t *testing.T, store *keys.MemoryStore, wrapper *keys.SRWWrapper, spaceID string) []byte {
	t.Helper()
	dk, err := keys.GenerateDK()
	if err != nil {
		t.Fatalf("GenerateDK: %v", err)
	}
	wrapped, err := wrapper.WrapSRW(dk)
	if err != nil {
		t.Fatalf("WrapSRW: %v", err)
	}
	if err := store.PutSRW(spaceID, wrapped); err != nil {
		t.Fatalf("PutSRW: %v", err)
	}
	return dk
}

// seedConfig binds the Space to the test target.
func seedConfig(t *testing.T, store *spacecfg.MemoryStore, spaceID, targetID string) {
	t.Helper()
	if _, err := store.Put(context.Background(), spacecfg.Config{
		SpaceID:  spaceID,
		TargetID: targetID,
		Enabled:  true,
	}); err != nil {
		t.Fatalf("spacecfg.Put: %v", err)
	}
}

func newSealer(t *testing.T) targets.CredSealer {
	t.Helper()
	twKey, err := keys.GenerateSRWKey()
	if err != nil {
		t.Fatalf("GenerateSRWKey: %v", err)
	}
	sealer, err := targets.NewCredSealer(twKey)
	if err != nil {
		t.Fatalf("NewCredSealer: %v", err)
	}
	return sealer
}

func newSRWWrapper(t *testing.T) *keys.SRWWrapper {
	t.Helper()
	srwKey, err := keys.GenerateSRWKey()
	if err != nil {
		t.Fatalf("GenerateSRWKey: %v", err)
	}
	w, err := keys.NewSRWWrapper(srwKey)
	if err != nil {
		t.Fatalf("NewSRWWrapper: %v", err)
	}
	t.Cleanup(w.Close)
	return w
}
