package snapshot

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kopia/kopia/fs"
)

var testMTime = time.Date(2021, 3, 14, 15, 9, 26, 0, time.UTC)

func treeSource(t *testing.T) *memSource {
	t.Helper()
	m := newMemSource("space-root")
	m.put("readme.txt", []byte("hello"), testMTime)
	m.put("docs/notes.txt", []byte("notes body"), testMTime)
	m.put("docs/nested/deep.bin", []byte("0123456789"), testMTime)
	return m
}

func TestRootEntry_Metadata(t *testing.T) {
	root := rootEntry(treeSource(t), fixedDirTime)

	if root.Name() != "space-root" {
		t.Fatalf("root name = %q", root.Name())
	}
	if !root.IsDir() || root.Mode() != dirMode {
		t.Fatalf("root mode = %v isDir=%v", root.Mode(), root.IsDir())
	}
	if root.Size() != 0 {
		t.Fatalf("directory size must be 0, got %d", root.Size())
	}
	if root.LocalFilesystemPath() != "" {
		t.Fatal("source entries must not claim a local filesystem path")
	}
	// Close must be idempotent (fs.Entry contract).
	root.Close()
	root.Close()
}

func TestIterate_SortedAndTyped(t *testing.T) {
	root := rootEntry(treeSource(t), fixedDirTime)

	var names []string
	err := fs.IterateEntries(context.Background(), root, func(_ context.Context, e fs.Entry) error {
		names = append(names, e.Name())
		switch e.Name() {
		case "docs":
			if _, ok := e.(fs.Directory); !ok {
				t.Fatalf("docs must be an fs.Directory, got %T", e)
			}
		case "readme.txt":
			f, ok := e.(fs.File)
			if !ok {
				t.Fatalf("readme.txt must be an fs.File, got %T", e)
			}
			if f.Size() != 5 || !f.ModTime().Equal(testMTime) {
				t.Fatalf("file metadata wrong: size=%d mtime=%v", f.Size(), f.ModTime())
			}
			if f.Mode() != fileMode {
				t.Fatalf("file mode = %v, want %v", f.Mode(), fileMode)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("IterateEntries: %v", err)
	}
	if len(names) != 2 || names[0] != "docs" || names[1] != "readme.txt" {
		t.Fatalf("children = %v, want [docs readme.txt] in sorted order", names)
	}
}

// Files must be fs.File, not fs.StreamingFile: kopia skips the size comparison
// when deciding to reuse a cached streaming entry, which would let a same-mtime
// content change slip through unnoticed.
func TestFilesAreNotStreamingFiles(t *testing.T) {
	root := rootEntry(treeSource(t), fixedDirTime)
	e, err := root.Child(context.Background(), "readme.txt")
	if err != nil {
		t.Fatalf("Child: %v", err)
	}
	if _, ok := e.(fs.StreamingFile); ok {
		t.Fatal("source files must not implement fs.StreamingFile")
	}
	if _, ok := e.(fs.File); !ok {
		t.Fatalf("source files must implement fs.File, got %T", e)
	}
}

func TestChild_NestedAndMissing(t *testing.T) {
	ctx := context.Background()
	root := rootEntry(treeSource(t), fixedDirTime)

	docs, err := root.Child(ctx, "docs")
	if err != nil {
		t.Fatalf("Child(docs): %v", err)
	}
	nested, err := docs.(fs.Directory).Child(ctx, "nested")
	if err != nil {
		t.Fatalf("Child(nested): %v", err)
	}
	deep, err := nested.(fs.Directory).Child(ctx, "deep.bin")
	if err != nil {
		t.Fatalf("Child(deep.bin): %v", err)
	}
	if deep.Size() != 10 {
		t.Fatalf("deep.bin size = %d, want 10", deep.Size())
	}

	if _, err := root.Child(ctx, "absent"); !errors.Is(err, fs.ErrEntryNotFound) {
		t.Fatalf("missing child error = %v, want fs.ErrEntryNotFound", err)
	}
}

func TestDirectory_ListsSourceOnce(t *testing.T) {
	ctx := context.Background()
	src := treeSource(t)
	listed := 0
	counting := &countingSource{memSource: src, onList: func() { listed++ }}

	root := rootEntry(counting, fixedDirTime)
	for range 3 {
		if _, err := root.Child(ctx, "readme.txt"); err != nil {
			t.Fatalf("Child: %v", err)
		}
	}
	if listed != 1 {
		t.Fatalf("directory listed %d times, want 1 (memoized)", listed)
	}
	if !root.SupportsMultipleIterations() {
		t.Fatal("source directories support repeated iteration")
	}
}

func TestDirectory_ListErrorPropagates(t *testing.T) {
	src := treeSource(t)
	sentinel := errors.New("cs3 unavailable")
	src.failList("docs", sentinel)

	root := rootEntry(src, fixedDirTime)
	docs, err := root.Child(context.Background(), "docs")
	if err != nil {
		t.Fatalf("Child(docs): %v", err)
	}
	if _, err := docs.(fs.Directory).Iterate(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("Iterate error = %v, want wrapped %v", err, sentinel)
	}
}

// findEntry resolves a slash-separated path below the source root.
func findEntry(t *testing.T, src Source, relPath string) fs.Entry {
	t.Helper()
	ctx := context.Background()
	var cur fs.Entry = rootEntry(src, fixedDirTime)
	for _, name := range strings.Split(relPath, "/") {
		dir, ok := cur.(fs.Directory)
		if !ok {
			t.Fatalf("%q is not a directory while resolving %q", cur.Name(), relPath)
		}
		e, err := dir.Child(ctx, name)
		if err != nil {
			t.Fatalf("find %q: %v", relPath, err)
		}
		cur = e
	}
	return cur
}

func openReader(t *testing.T, src Source, relPath string) fs.Reader {
	t.Helper()
	r, err := findEntry(t, src, relPath).(fs.File).Open(context.Background())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return r
}

func TestReader_LazyOpen(t *testing.T) {
	src := treeSource(t)
	r := openReader(t, src, "readme.txt")
	defer func() { _ = r.Close() }()

	if got := src.openCount("readme.txt"); got != 0 {
		t.Fatalf("Open() must not fetch bytes yet, opens=%d", got)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("read %q, want hello", buf)
	}
	if got := src.openCount("readme.txt"); got != 1 {
		t.Fatalf("opens = %d, want 1", got)
	}
}

func TestReader_SeekBeforeReadCostsNoFetch(t *testing.T) {
	src := treeSource(t)
	r := openReader(t, src, "docs/nested/deep.bin")
	defer func() { _ = r.Close() }()

	if _, err := r.Seek(4, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if got := src.openCount("docs/nested/deep.bin"); got != 0 {
		t.Fatalf("seek before read must not fetch, opens=%d", got)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "456789" {
		t.Fatalf("read %q, want 456789", got)
	}
	if n := src.openCount("docs/nested/deep.bin"); n != 1 {
		t.Fatalf("opens = %d, want 1", n)
	}
}

func TestReader_SeekAfterReadReopens(t *testing.T) {
	src := treeSource(t)
	r := openReader(t, src, "docs/nested/deep.bin")
	defer func() { _ = r.Close() }()

	buf := make([]byte, 2)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := r.Seek(8, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	rest, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(rest) != "89" {
		t.Fatalf("read %q, want 89", rest)
	}
	if n := src.openCount("docs/nested/deep.bin"); n != 2 {
		t.Fatalf("opens = %d, want 2 (reopen after seek)", n)
	}
}

func TestReader_SeekWhences(t *testing.T) {
	src := treeSource(t)
	r := openReader(t, src, "docs/nested/deep.bin")
	defer func() { _ = r.Close() }()

	if pos, err := r.Seek(3, io.SeekCurrent); err != nil || pos != 3 {
		t.Fatalf("SeekCurrent = %d, %v", pos, err)
	}
	if pos, err := r.Seek(-2, io.SeekEnd); err != nil || pos != 8 {
		t.Fatalf("SeekEnd = %d, %v", pos, err)
	}
	if pos, err := r.Seek(0, io.SeekStart); err != nil || pos != 0 {
		t.Fatalf("SeekStart = %d, %v", pos, err)
	}
	// Seeking to the current position is a no-op and must not error.
	if pos, err := r.Seek(0, io.SeekStart); err != nil || pos != 0 {
		t.Fatalf("redundant seek = %d, %v", pos, err)
	}
	if _, err := r.Seek(-1, io.SeekStart); err == nil {
		t.Fatal("negative position must error")
	}
	if _, err := r.Seek(0, 99); err == nil {
		t.Fatal("invalid whence must error")
	}
}

func TestReader_EntryAndCloseIdempotent(t *testing.T) {
	src := treeSource(t)
	r := openReader(t, src, "readme.txt")

	e, err := r.Entry()
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if e.Name() != "readme.txt" || e.Size() != 5 {
		t.Fatalf("entry = %+v", e)
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, errClosedReader) {
		t.Fatalf("read after close = %v, want errClosedReader", err)
	}
	if _, err := r.Seek(0, io.SeekStart); !errors.Is(err, errClosedReader) {
		t.Fatalf("seek after close = %v, want errClosedReader", err)
	}
}

func TestReader_OpenErrorSurfaces(t *testing.T) {
	src := treeSource(t)
	sentinel := errors.New("cs3 download failed")
	src.failOpen("readme.txt", sentinel)

	r := openReader(t, src, "readme.txt")
	defer func() { _ = r.Close() }()

	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, sentinel) {
		t.Fatalf("read error = %v, want wrapped %v", err, sentinel)
	}
}

func TestEntryModeIsStableAcrossWalks(t *testing.T) {
	// kopia compares Mode/Owner/Device when reusing cached entries, so these
	// must not drift between runs.
	src := treeSource(t)
	first := rootEntry(src, fixedDirTime)
	second := rootEntry(src, fixedDirTime)

	ctx := context.Background()
	a, err := first.Child(ctx, "readme.txt")
	if err != nil {
		t.Fatalf("Child: %v", err)
	}
	b, err := second.Child(ctx, "readme.txt")
	if err != nil {
		t.Fatalf("Child: %v", err)
	}
	if a.Mode() != b.Mode() || a.Owner() != b.Owner() || a.Device() != b.Device() {
		t.Fatal("entry mode/owner/device must be stable across walks")
	}
	if a.Mode()&os.ModeType != 0 {
		t.Fatalf("regular files must have no type bits set, got %v", a.Mode())
	}
}

// countingSource wraps memSource to count List calls.
type countingSource struct {
	*memSource
	onList func()
}

func (c *countingSource) List(ctx context.Context, dir string) ([]Node, error) {
	c.onList()
	return c.memSource.List(ctx, dir)
}
