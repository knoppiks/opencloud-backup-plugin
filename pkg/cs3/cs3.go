// Package cs3 wraps the OpenCloud CS3 gateway so the rest of the service reads
// Spaces and file streams through a single, testable interface.
//
// The unattended worker authenticates with an OpenCloud service account
// (CS3 auth type "serviceaccounts"; see decisions.md #11 and phase-0-findings.md
// Spike 3). The concrete client is implemented in Phase 2; this file defines the
// boundary interface only.
package cs3

import (
	"context"
	"io"
)

// ResourceID mirrors the CS3 provider.ResourceId triple. The space root's
// resource id is required verbatim to build the space-relative references the
// data path needs; OpenCloud space ids are composite, so reconstructing the
// triple from the space id alone is not reliable (phase-0-findings.md Spike 3).
type ResourceID struct {
	StorageID string
	SpaceID   string
	OpaqueID  string
}

// Zero reports whether the resource id carries no addressing information.
func (r ResourceID) Zero() bool {
	return r.StorageID == "" && r.SpaceID == "" && r.OpaqueID == ""
}

// Space identifies an OpenCloud storage space, the unit of backup, keying, and
// scheduling (decisions.md #6).
type Space struct {
	// ID is the CS3 space (storage) ID.
	ID string
	// Name is the human-readable space name.
	Name string
	// Type is the space type ("personal", "project", ...).
	Type string
	// Owner is the owning user's opaque ID (empty for some project spaces).
	Owner string
	// Root is the space root's resource id, used to build space-relative
	// references for the walk and download paths.
	Root ResourceID
	// Members maps principal ID -> role for shared spaces, derived from the
	// space's Opaque grants map (phase-0-findings.md Spike 3). Empty for a
	// personal space.
	Members map[string]string
}

// Entry is a single file or directory encountered while walking a Space.
type Entry struct {
	// Path is the space-relative, slash-separated path with no leading "./" or
	// "/", e.g. "photos/img.jpg". The empty string denotes the space root.
	Path string
	// IsDir reports whether the entry is a directory.
	IsDir bool
	// Size is the file size in bytes (0 for directories).
	Size int64
	// MTimeUnix is the modification time (seconds); mtime is in scope
	// (decisions.md #4).
	MTimeUnix int64
}

// SpaceReader is the read-only boundary onto OpenCloud. Implementations are
// expected to be safe for use by a single worker run.
//
// InitiateFileDownload/OpenFile must be given a space-relative reference built
// from the space root plus relative path; a bare file id fails (phase-0-findings
// Spike 3, "Critical gotcha").
type SpaceReader interface {
	// ListSpaces returns the spaces the worker credential may back up.
	ListSpaces(ctx context.Context) ([]Space, error)
	// ListDir returns the direct children of one space-relative directory.
	// The empty path denotes the space root. Returned entries carry paths
	// relative to the space root.
	ListDir(ctx context.Context, space Space, relDir string) ([]Entry, error)
	// Walk visits every entry under the space root, calling fn for each.
	Walk(ctx context.Context, space Space, fn func(Entry) error) error
	// OpenFile streams the bytes of one space-relative file starting at offset.
	// The caller closes the returned reader.
	OpenFile(ctx context.Context, space Space, relPath string, offset int64) (io.ReadCloser, error)
}
