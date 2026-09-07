package cs3state

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/state"
	"opencloud-backup-plugin/pkg/state/statetest"
)

// fakeSpace is an in-memory stand-in for a CS3 Space with the behaviours this
// store actually depends on: directories must exist before a file can be
// written, uploads refuse to clobber, and listings are per-directory.
type fakeSpace struct {
	mu    sync.Mutex
	space cs3.Space
	files map[string][]byte
	dirs  map[string]struct{}

	// listErr, if set, fails ListSpaces.
	listErr error
	// clobbers makes Upload overwrite silently instead of refusing, which is
	// the other behaviour reva could have (see the overwrite-semantics
	// integration test). Create must be safe either way.
	clobbers bool
	// uploads counts writes, so tests can see delete-then-write happening.
	uploads int
	deletes int
}

func newFakeSpace() *fakeSpace {
	return &fakeSpace{
		space: cs3.Space{ID: "state-space", Name: "Service state", Type: "project"},
		files: make(map[string][]byte),
		dirs:  map[string]struct{}{"": {}},
	}
}

func (f *fakeSpace) ListSpaces(context.Context) ([]cs3.Space, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return []cs3.Space{f.space, {ID: "someone-elses", Name: "Alice"}}, nil
}

func (f *fakeSpace) ListDir(_ context.Context, _ cs3.Space, relDir string) ([]cs3.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	relDir = strings.Trim(relDir, "/")
	if _, ok := f.dirs[relDir]; !ok {
		return nil, fmt.Errorf("cs3 ListContainer: code=CODE_NOT_FOUND")
	}

	var out []cs3.Entry
	for name := range f.dirs {
		if name != "" && path.Dir(name) == dirKey(relDir) {
			out = append(out, cs3.Entry{Path: name, IsDir: true})
		}
	}
	for name, data := range f.files {
		if path.Dir(name) == dirKey(relDir) {
			out = append(out, cs3.Entry{Path: name, Size: int64(len(data))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// dirKey normalises the root directory so path.Dir comparisons work.
func dirKey(relDir string) string {
	if relDir == "" {
		return "."
	}
	return relDir
}

func (f *fakeSpace) Walk(context.Context, cs3.Space, func(cs3.Entry) error) error { return nil }

func (f *fakeSpace) OpenFile(_ context.Context, _ cs3.Space, relPath string, _ int64) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[strings.Trim(relPath, "/")]
	if !ok {
		return nil, fmt.Errorf("cs3 Stat: code=CODE_NOT_FOUND")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeSpace) MakeDir(_ context.Context, _ cs3.Space, relDir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	relDir = strings.Trim(relDir, "/")
	if parent := path.Dir(relDir); parent != "." && parent != relDir {
		if _, ok := f.dirs[parent]; !ok {
			return fmt.Errorf("cs3 CreateContainer: code=CODE_NOT_FOUND")
		}
	}
	f.dirs[relDir] = struct{}{}
	return nil
}

func (f *fakeSpace) Upload(_ context.Context, _ cs3.Space, relPath string, size int64, _ time.Time, r io.Reader) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	relPath = strings.Trim(relPath, "/")
	if _, exists := f.files[relPath]; exists && !f.clobbers {
		// Mirrors the real writer: a restore must never clobber.
		return fmt.Errorf("%w: %s", cs3.ErrAlreadyExists, relPath)
	}
	if _, ok := f.dirs[path.Dir(relPath)]; !ok && path.Dir(relPath) != "." {
		return fmt.Errorf("cs3 InitiateFileUpload: code=CODE_NOT_FOUND")
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return fmt.Errorf("announced %d bytes, wrote %d", size, len(data))
	}
	f.files[relPath] = data
	f.uploads++
	return nil
}

func (f *fakeSpace) Delete(_ context.Context, _ cs3.Space, relPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	relPath = strings.Trim(relPath, "/")
	if _, ok := f.files[relPath]; !ok {
		return fmt.Errorf("%w: %s", cs3.ErrNotFound, relPath)
	}
	delete(f.files, relPath)
	f.deletes++
	return nil
}

func newStore(t *testing.T) (*Store, *fakeSpace) {
	t.Helper()
	fake := newFakeSpace()
	store, err := New(fake, Options{SpaceID: "state-space"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store, fake
}

func TestStoreContract(t *testing.T) {
	statetest.RunStoreContract(t, func(t *testing.T) state.Store {
		store, _ := newStore(t)
		return store
	})
}

// Everything must live under the configured folder, so the service's state
// cannot end up scattered through a Space.
func TestStore_WritesUnderThePrefix(t *testing.T) {
	ctx := context.Background()
	fake := newFakeSpace()
	store, err := New(fake, Options{SpaceID: "state-space", Prefix: "custom-prefix"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := store.Create(ctx, "jobs/space/1", []byte("{}")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, ok := fake.files["custom-prefix/jobs/space/1"]; !ok {
		t.Fatalf("files = %v", keysOf(fake.files))
	}
}

// A key with a traversal attempt in it must not escape the prefix. Keys are
// built by pkg/state, which escapes segments, so this is a belt-and-braces
// check on the two of them together.
func TestStore_EscapedKeysCannotEscapeThePrefix(t *testing.T) {
	ctx := context.Background()
	store, fake := newStore(t)

	key := state.Key("jobs", "../../etc", "1")
	if err := store.Create(ctx, key, []byte("{}")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for name := range fake.files {
		if !strings.HasPrefix(name, DefaultPrefix+"/") {
			t.Fatalf("wrote outside the state folder: %q", name)
		}
		for _, segment := range strings.Split(name, "/") {
			if segment == ".." || segment == "." {
				t.Fatalf("wrote a traversal path: %q", name)
			}
		}
	}
}

// Replacing a document has to work even though the upload path refuses to
// clobber.
func TestStore_ReplaceDeletesThenWrites(t *testing.T) {
	ctx := context.Background()
	store, fake := newStore(t)

	if err := store.Replace(ctx, "docs/one", []byte("first")); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if err := store.Replace(ctx, "docs/one", []byte("second")); err != nil {
		t.Fatalf("Replace again: %v", err)
	}
	if fake.deletes != 1 || fake.uploads != 2 {
		t.Fatalf("deletes = %d, uploads = %d", fake.deletes, fake.uploads)
	}
	got, err := store.Get(ctx, "docs/one")
	if err != nil || string(got) != "second" {
		t.Fatalf("Get = %q (%v)", got, err)
	}
}

// The guard that matters: on a server that overwrites silently, a create must
// still refuse rather than destroy the stored document. This is what makes
// append-only key envelopes safe without knowing what reva does.
func TestStore_CreateRefusesEvenWhenUploadsClobber(t *testing.T) {
	ctx := context.Background()
	store, fake := newStore(t)
	fake.clobbers = true

	if err := store.Create(ctx, "keyenvelopes/space/rk/1", []byte("envelope")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Create(ctx, "keyenvelopes/space/rk/1", []byte("clobber")); !state.IsExists(err) {
		t.Fatalf("Create again = %v, want already-exists", err)
	}
	got, err := store.Get(ctx, "keyenvelopes/space/rk/1")
	if err != nil || string(got) != "envelope" {
		t.Fatalf("Get = %q (%v), want the original envelope", got, err)
	}
	if fake.deletes != 0 {
		t.Fatalf("deletes = %d, want none: a create must never remove anything", fake.deletes)
	}
}

// The state Space holds the only server-side copy of every wrapped Data Key. A
// Space an end user can reach is a Space an end user can empty.
func TestStore_CheckRefusesASpaceUsersCanReach(t *testing.T) {
	ctx := context.Background()

	t.Run("a dedicated project space is accepted", func(t *testing.T) {
		store, _ := newStore(t)
		if err := store.Check(ctx); err != nil {
			t.Fatalf("Check: %v", err)
		}
	})

	t.Run("a personal space is refused", func(t *testing.T) {
		fake := newFakeSpace()
		fake.space.Type = "personal"
		store, err := New(fake, Options{SpaceID: "state-space"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := store.Check(ctx); err == nil {
			t.Fatal("a personal space must be refused")
		}
	})

	t.Run("a space with a member grant is refused, without naming the member", func(t *testing.T) {
		fake := newFakeSpace()
		fake.space.Members = map[string]cs3.Member{"user-alice": {Role: cs3.RoleManager}}
		store, err := New(fake, Options{SpaceID: "state-space"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		err = store.Check(ctx)
		if err == nil {
			t.Fatal("a space with members must be refused")
		}
		if strings.Contains(err.Error(), "user-alice") {
			t.Fatalf("the error names a member: %v", err)
		}
	})
}

func TestStore_RequiresAConfiguredSpace(t *testing.T) {
	if _, err := New(newFakeSpace(), Options{}); err == nil {
		t.Fatal("a missing space id must be rejected")
	}
	if _, err := New(nil, Options{SpaceID: "s"}); err == nil {
		t.Fatal("a missing client must be rejected")
	}
}

func TestStore_UnknownSpaceIsAClearError(t *testing.T) {
	store, err := New(newFakeSpace(), Options{SpaceID: "no-such-space"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := store.Get(context.Background(), "docs/one"); err == nil {
		t.Fatal("want an error")
	} else if state.IsNotFound(err) {
		t.Fatalf("a missing state space must not look like a missing key: %v", err)
	}
}

func TestStore_PropagatesBackendFailures(t *testing.T) {
	fake := newFakeSpace()
	fake.listErr = errors.New("gateway unreachable")
	store, err := New(fake, Options{SpaceID: "state-space"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := store.Create(context.Background(), "docs/one", []byte("{}")); err == nil {
		t.Fatal("a broken gateway must surface as an error, not silent success")
	}
}

// The store is used from the scheduler, the API and background runs at once.
func TestStore_IsConcurrencySafe(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("jobs/space/%02d", i)
			if err := store.Create(ctx, key, []byte("{}")); err != nil {
				t.Errorf("Create %s: %v", key, err)
			}
		}()
	}
	wg.Wait()

	keys, err := store.List(ctx, "jobs/space")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 16 {
		t.Fatalf("keys = %d, want 16", len(keys))
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
