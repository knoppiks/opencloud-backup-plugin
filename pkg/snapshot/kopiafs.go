package snapshot

// Adapter from our kopia-free Source tree onto kopia's fs.Entry family. This is
// Phase 4 "Option B": kopia's snapshot walker pulls entries on demand and
// streams file bytes straight from CS3, so no Space is ever staged on disk.
//
// Two deliberate choices, both load-bearing:
//
//   - Files are exposed as fs.File (not fs.StreamingFile). kopia's cache-hit
//     check compares size for fs.File but *skips* size for fs.StreamingFile
//     (snapshot/upload.findCachedEntry). Using fs.File keeps the same
//     change-detection strength kopia gives a local filesystem source.
//   - Mode, owner and device are constant. They take part in kopia's cache-hit
//     comparison, so they must be stable across runs; permissions are out of
//     backup scope anyway (decisions.md #4).
//
// The kopia fs interfaces are not a stability-guaranteed API, so this adapter
// stays thin and the kopia version is pinned (phase-4 risk note).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sync"
	"time"

	"github.com/kopia/kopia/fs"
)

const (
	// dirMode / fileMode are the constant modes reported to kopia. They must not
	// vary between runs: kopia compares Mode() when deciding whether a previously
	// snapshotted entry can be reused.
	dirMode  = os.ModeDir | 0o755
	fileMode = os.FileMode(0o644)
)

// errClosedReader is returned when a reader is used after Close.
var errClosedReader = errors.New("snapshot: read after close")

// sourceEntry is the metadata shared by source-backed directories and files.
type sourceEntry struct {
	src Source
	// path is the source-relative path; "" is the root.
	path string
	node Node
}

func (e *sourceEntry) Name() string { return e.node.Name }
func (e *sourceEntry) IsDir() bool  { return e.node.IsDir }

func (e *sourceEntry) Mode() os.FileMode {
	if e.node.IsDir {
		return dirMode
	}
	return fileMode
}

func (e *sourceEntry) ModTime() time.Time { return e.node.ModTime }

func (e *sourceEntry) Size() int64 {
	if e.node.IsDir {
		return 0
	}
	return e.node.Size
}

func (e *sourceEntry) Sys() any                    { return nil }
func (e *sourceEntry) Owner() fs.OwnerInfo         { return fs.OwnerInfo{} }
func (e *sourceEntry) Device() fs.DeviceInfo       { return fs.DeviceInfo{} }
func (e *sourceEntry) LocalFilesystemPath() string { return "" }

// Close releases per-entry resources. Source entries hold none; it must be
// idempotent (fs.Entry contract).
func (e *sourceEntry) Close() {}

// sourceDir adapts a Source directory to fs.Directory.
type sourceDir struct {
	sourceEntry

	mu sync.Mutex
	// children caches one successful listing so Child and Iterate do not hit the
	// backing store repeatedly for the same directory.
	children []fs.Entry
	listed   bool
}

var _ fs.Directory = (*sourceDir)(nil)

// SupportsMultipleIterations reports true: the backing Source may be listed
// repeatedly (CS3 ListContainer is a plain read).
func (d *sourceDir) SupportsMultipleIterations() bool { return true }

// Iterate returns an iterator over the directory's children, sorted by name as
// kopia's own sources are.
func (d *sourceDir) Iterate(ctx context.Context) (fs.DirectoryIterator, error) {
	entries, err := d.list(ctx)
	if err != nil {
		return nil, err
	}
	return fs.StaticIterator(append([]fs.Entry{}, entries...), nil), nil
}

// Child returns one named child, or fs.ErrEntryNotFound.
func (d *sourceDir) Child(ctx context.Context, name string) (fs.Entry, error) {
	entries, err := d.list(ctx)
	if err != nil {
		return nil, err
	}
	if e := fs.FindByName(entries, name); e != nil {
		return e, nil
	}
	return nil, fs.ErrEntryNotFound
}

// list returns the memoized child entries, listing the source on first use.
func (d *sourceDir) list(ctx context.Context) ([]fs.Entry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.listed {
		return d.children, nil
	}

	nodes, err := d.src.List(ctx, d.path)
	if err != nil {
		return nil, fmt.Errorf("snapshot: list %q: %w", d.path, err)
	}

	entries := make([]fs.Entry, 0, len(nodes))
	for _, n := range nodes {
		entries = append(entries, newEntry(d.src, path.Join(d.path, n.Name), n))
	}
	fs.Sort(entries)

	d.children, d.listed = entries, true
	return entries, nil
}

// sourceFile adapts a Source file to fs.File.
type sourceFile struct {
	sourceEntry
}

var _ fs.File = (*sourceFile)(nil)

// Open returns a lazily-connected reader. No byte is fetched until the first
// Read, so a Seek issued straight after Open costs nothing.
func (f *sourceFile) Open(ctx context.Context) (fs.Reader, error) {
	return &sourceReader{ctx: ctx, file: f}, nil
}

// newEntry builds the fs.Entry for one node.
func newEntry(src Source, p string, n Node) fs.Entry {
	e := sourceEntry{src: src, path: p, node: n}
	if n.IsDir {
		return &sourceDir{sourceEntry: e}
	}
	return &sourceFile{sourceEntry: e}
}

// rootEntry builds the fs.Directory for a Source's root.
func rootEntry(src Source, modTime time.Time) fs.Directory {
	return &sourceDir{sourceEntry: sourceEntry{
		src:  src,
		path: "",
		node: Node{Name: src.Name(), IsDir: true, ModTime: modTime},
	}}
}

// sourceReader streams one file's bytes. It is not safe for concurrent use; the
// uploader gives each reader to a single goroutine.
//
// The context is captured at Open because fs.Reader mirrors io.ReadCloser and
// has no per-call context — the same shape kopia's own virtual sources use.
type sourceReader struct {
	ctx  context.Context
	file *sourceFile

	rc     io.ReadCloser
	pos    int64
	closed bool
}

var _ fs.Reader = (*sourceReader)(nil)

// Read streams from the current position, connecting on first use.
func (r *sourceReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, errClosedReader
	}
	if r.rc == nil {
		rc, err := r.file.src.Open(r.ctx, r.file.path, r.pos)
		if err != nil {
			return 0, fmt.Errorf("snapshot: open %q at %d: %w", r.file.path, r.pos, err)
		}
		r.rc = rc
	}
	n, err := r.rc.Read(p)
	r.pos += int64(n)
	return n, err
}

// Seek repositions the stream. Because the reader is lazy, seeking before the
// first Read simply chooses the offset the stream will start at; seeking after
// reading reopens the stream at the new position.
func (r *sourceReader) Seek(offset int64, whence int) (int64, error) {
	if r.closed {
		return 0, errClosedReader
	}

	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.pos + offset
	case io.SeekEnd:
		abs = r.file.Size() + offset
	default:
		return 0, fmt.Errorf("snapshot: invalid whence %d", whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("snapshot: negative seek position %d", abs)
	}
	if abs == r.pos {
		return abs, nil
	}

	if r.rc != nil {
		if err := r.rc.Close(); err != nil {
			return 0, fmt.Errorf("snapshot: close before seek: %w", err)
		}
		r.rc = nil
	}
	r.pos = abs
	return abs, nil
}

// Entry returns the file's metadata as captured at walk time.
func (r *sourceReader) Entry() (fs.Entry, error) { return r.file, nil }

// Close releases the underlying stream. It is safe to call more than once.
func (r *sourceReader) Close() error {
	r.closed = true
	if r.rc == nil {
		return nil
	}
	rc := r.rc
	r.rc = nil
	return rc.Close()
}
