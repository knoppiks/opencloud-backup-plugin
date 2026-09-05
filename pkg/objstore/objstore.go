// Package objstore is a minimal object-store boundary for the few things this
// service stores next to — but not inside — a kopia repository.
//
// kopia owns everything under a Space's repo prefix; nothing here ever writes
// there (see snapshot.RepoPrefix vs snapshot.EnvelopeKey). The two users are:
//
//   - the backup worker, publishing a Space's RK-wrapped key envelope to the
//     target so a Take-Out is self-contained (Phase 5, Path A), and
//   - the Take-Out CLI, copying a Space's ciphertext objects out of S3 with no
//     OpenCloud dependency and no key input at all.
//
// It is an interface so both can be exercised against a local directory in unit
// tests, with S3 reserved for integration tests (AGENTS.md testability rule).
package objstore

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound is returned when no object exists at a key.
var ErrNotFound = errors.New("objstore: object not found")

// ObjectInfo describes one stored object. It carries no credentials and no key
// material.
type ObjectInfo struct {
	// Key is the full object key, including any prefix.
	Key string
	// Size is the object size in bytes.
	Size int64
}

// Store is the read/write object boundary.
//
// Implementations must be safe for concurrent use.
type Store interface {
	// Get streams the object at key. The caller closes the reader.
	// It returns ErrNotFound when the key does not exist.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Put stores body at key. Implementations read body to EOF.
	Put(ctx context.Context, key string, body []byte) error
	// List returns every object whose key starts with prefix.
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)
}
