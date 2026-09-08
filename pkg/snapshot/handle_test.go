package snapshot

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kopia/kopia/repo/blob"
)

// testSecret is what a driver's ConnectionInfo would carry into
// repository.config if nothing stopped it. No file under the work directory may
// contain it.
const testSecret = "SECRET-ACCESS-KEY-DO-NOT-PERSIST"

// leakyOpener wraps a real opener with a storage whose ConnectionInfo carries a
// secret, standing in for kopia's S3 driver (whose ConnectionInfo carries the
// secret access key in clear text).
//
// Its storage type is deliberately not registered with kopia: if the engine ever
// stopped substituting the handle, the connection would not merely leak the
// secret, it would fail to reopen — so this fake cannot be silently bypassed.
type leakyOpener struct {
	inner StorageOpener
}

func (o leakyOpener) Open(ctx context.Context, r Repo, createIfMissing bool) (blob.Storage, error) {
	st, err := o.inner.Open(ctx, r, createIfMissing)
	if err != nil {
		return nil, err
	}
	return leakyStorage{Storage: st}, nil
}

type leakyStorage struct{ blob.Storage }

func (leakyStorage) ConnectionInfo() blob.ConnectionInfo {
	return blob.ConnectionInfo{
		Type:   "test-leaky-driver",
		Config: map[string]string{"secretAccessKey": testSecret},
	}
}

// inspectSource runs a callback the first time the backup reads a file, i.e.
// while the repository is connected and the work directory is fully populated.
type inspectSource struct {
	*memSource
	once    sync.Once
	inspect func()
}

func (s *inspectSource) Open(ctx context.Context, p string, offset int64) (io.ReadCloser, error) {
	s.once.Do(s.inspect)
	return s.memSource.Open(ctx, p, offset)
}

// decisions.md #14: target credentials are plaintext in worker memory only. The
// per-run work directory is the one place they used to escape to.
func TestSnapshot_WorkDirNeverHoldsTargetCredentials(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	e, err := NewEngine(leakyOpener{inner: FilesystemOpener{Root: t.TempDir()}}, EngineOptions{WorkDir: workDir})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	var files map[string]string
	src := &inspectSource{
		memSource: treeSource(t),
		inspect:   func() { files = readTree(t, workDir) },
	}

	if _, err := e.Snapshot(ctx, testRepo(t, "space-1"), src); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	var configs int
	for name, content := range files {
		if strings.Contains(content, testSecret) {
			t.Fatalf("%s contains the target's credentials", name)
		}
		if filepath.Base(name) == "repository.config" {
			configs++
			if !strings.Contains(content, handleStorageType) {
				t.Fatalf("%s does not connect through a handle: %s", name, content)
			}
		}
	}
	if configs == 0 {
		t.Fatal("no repository.config was written during the run; the test proves nothing")
	}
}

// A run cleans up after itself, so nothing is left to find afterwards either.
func TestSnapshot_WorkDirIsEmptyAfterARun(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	e, err := NewEngine(FilesystemOpener{Root: t.TempDir()}, EngineOptions{WorkDir: workDir})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := e.Snapshot(ctx, testRepo(t, "space-1"), treeSource(t)); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if left := readTree(t, workDir); len(left) != 0 {
		t.Fatalf("work directory still holds %d files after the run", len(left))
	}
}

func TestHandleStorage_ConnectionInfoCarriesOnlyTheHandle(t *testing.T) {
	st := handleStorage{Storage: leakyStorage{}, handle: "abc123"}

	raw, err := json.Marshal(st.ConnectionInfo())
	if err != nil {
		t.Fatalf("marshal connection info: %v", err)
	}
	got := string(raw)
	if strings.Contains(got, testSecret) {
		t.Fatalf("connection info leaked the wrapped driver's secret: %s", got)
	}
	if !strings.Contains(got, "abc123") || !strings.Contains(got, handleStorageType) {
		t.Fatalf("connection info = %s, want the handle and its storage type", got)
	}
}

func TestHandleRegistry_HandleLivesOnlyForTheRun(t *testing.T) {
	ctx := context.Background()
	reg := newHandleRegistry()
	opened := 0
	open := func(context.Context, bool) (blob.Storage, error) {
		opened++
		return leakyStorage{}, nil
	}

	handle, release, err := reg.register(open)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := reg.open(ctx, handle, false); err != nil {
		t.Fatalf("open a live handle: %v", err)
	}
	if opened != 1 {
		t.Fatalf("opener called %d times, want 1", opened)
	}

	release()
	if _, err := reg.open(ctx, handle, false); err == nil {
		t.Fatal("a released handle must not open anything")
	}
	if _, err := reg.open(ctx, "never-issued", false); err == nil {
		t.Fatal("an unknown handle must not open anything")
	}
}

func TestHandleRegistry_HandlesAreDistinct(t *testing.T) {
	reg := newHandleRegistry()
	open := func(context.Context, bool) (blob.Storage, error) { return leakyStorage{}, nil }

	first, release1, err := reg.register(open)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer release1()
	second, release2, err := reg.register(open)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer release2()

	if first == second {
		t.Fatal("two runs must not share a handle")
	}
	if _, _, err := reg.register(nil); err == nil {
		t.Fatal("a nil opener must be rejected")
	}
}

// readTree returns every regular file under root, keyed by path.
func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[path] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}
