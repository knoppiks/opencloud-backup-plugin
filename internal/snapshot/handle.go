package snapshot

// Keeping target credentials out of kopia's on-disk configuration.
//
// kopia will not open a repository without a local config file: repo.Connect
// serialises the storage's ConnectionInfo into repository.config, and repo.Open
// reads it back. For the S3 driver that ConnectionInfo contains the secret
// access key in clear text — kopia's `sensitive` struct tag only affects how the
// value is *displayed*, not whether it is written. decisions.md #14 says target
// credentials exist in plaintext only in worker memory for the duration of a
// run; a file that survives a SIGKILL contradicts that.
//
// kopia's own extension point fixes it without patching kopia. A storage type
// registered through blob.AddSupportedStorage supplies its own ConnectionInfo,
// so ours carries nothing but an opaque, random, per-run handle. The credentials
// stay in this process's memory, keyed by that handle, and what lands on disk is
// a string that means nothing to anyone who finds it — on any filesystem, after
// any kind of crash.
//
// The registry has to be a package-level variable because kopia's storage
// registry is itself process-global; that is the whole cost of the approach, and
// it is contained in this file. The type is testable on its own.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/kopia/kopia/repo/blob"
)

// handleStorageType is the storage type name written to repository.config. It
// is meaningless outside a running instance of this service, which is the point.
const handleStorageType = "opencloud-backup-handle"

// handleConfig is everything this service is willing to write to disk about
// where a repository lives: a lookup key into memory.
type handleConfig struct {
	Handle string `json:"handle"`
}

// storageOpenFunc reopens the blob storage behind a handle. It closes over the
// in-memory credentials rather than carrying them.
type storageOpenFunc func(ctx context.Context, createIfMissing bool) (blob.Storage, error)

// handleRegistry maps live handles to the openers that can serve them. Entries
// exist only while a run holds them.
type handleRegistry struct {
	mu      sync.Mutex
	entries map[string]storageOpenFunc
}

func newHandleRegistry() *handleRegistry {
	return &handleRegistry{entries: map[string]storageOpenFunc{}}
}

// register stores open under a fresh handle and returns the handle together with
// the function that removes it. The caller must always call release: a handle
// that outlives its run is a way to reach a target after the run that was
// allowed to reach it has finished.
func (r *handleRegistry) register(open storageOpenFunc) (string, func(), error) {
	if open == nil {
		return "", nil, fmt.Errorf("snapshot: storage opener required")
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("snapshot: generate storage handle: %w", err)
	}
	handle := hex.EncodeToString(raw)

	r.mu.Lock()
	r.entries[handle] = open
	r.mu.Unlock()

	return handle, func() {
		r.mu.Lock()
		delete(r.entries, handle)
		r.mu.Unlock()
	}, nil
}

// open resolves a handle. An unknown handle is a config file that outlived the
// process that wrote it — a leftover, not a repository this process may open.
func (r *handleRegistry) open(ctx context.Context, handle string, createIfMissing bool) (blob.Storage, error) {
	r.mu.Lock()
	fn, ok := r.entries[handle]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("snapshot: storage handle is not valid in this process")
	}
	return fn(ctx, createIfMissing)
}

// storageHandles is the process-wide registry kopia's global storage registry
// resolves against.
var storageHandles = newHandleRegistry()

func init() {
	blob.AddSupportedStorage(handleStorageType, handleConfig{},
		func(ctx context.Context, opt *handleConfig, isCreate bool) (blob.Storage, error) {
			return storageHandles.open(ctx, opt.Handle, isCreate)
		})
}

// handleStorage is a blob.Storage that lies about one thing: how to reconnect to
// it. Every other call passes straight through to the real driver.
type handleStorage struct {
	blob.Storage
	handle string
}

var _ blob.Storage = handleStorage{}

// ConnectionInfo returns the handle instead of the driver's own settings, so
// nothing kopia persists can be used to reach the target.
func (s handleStorage) ConnectionInfo() blob.ConnectionInfo {
	return blob.ConnectionInfo{
		Type:   handleStorageType,
		Config: &handleConfig{Handle: s.handle},
	}
}
