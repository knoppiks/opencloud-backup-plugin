// Package cs3state stores the service's own state inside OpenCloud, over CS3.
//
// This is a deliberate choice with real trade-offs, so they are written down
// here rather than discovered later:
//
//   - **Why.** The deployment already has one durable, backed-up, operated
//     storage system: OpenCloud's. Adding a second one (a database and its
//     volume) for a family-scale plugin is infrastructure the operator did not
//     ask for. State goes where the data already lives.
//
//   - **What it costs.** A CS3 Space is a filesystem, not a database: no
//     transactions, no compare-and-set. Nothing in this package pretends
//     otherwise. Mutual exclusion between runs is process-local (pkg/jobs), and
//     the durable lease exists only so a *crashed* process can be cleaned up
//     after. That is sound for the locked single-instance deployment and unsound
//     for a scaled-out one. Running two instances against one state Space is
//     unsupported.
//
//   - **Where.** A dedicated Space, configured by the operator, that no end user
//     is a member of. It must not be a user's Space: everything here is
//     service-internal, a member could delete it, and the backup service's
//     memory is not a user's document. What is stored is metadata plus already
//     wrapped envelopes (SRW/TW ciphertext) — never plaintext key material — but
//     that is a defence in depth, not a licence to put it somewhere readable.
//
// Keys map to paths: "jobs/space/123" becomes "<prefix>/jobs/space/123". Key
// segments are pre-escaped by pkg/state to a safe alphabet, so no key can walk
// out of the prefix.
package cs3state

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"time"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/state"
)

// DefaultPrefix is the folder the service keeps its state in.
const DefaultPrefix = ".backup-service-state"

// maxDocumentBytes caps a single state document. State documents are small
// records; anything larger means a bug, and reading unbounded data out of a
// Space into memory is not something to discover in production.
const maxDocumentBytes = 1 << 20

// SpaceClient is the CS3 surface this store needs.
type SpaceClient interface {
	cs3.SpaceReader
	cs3.SpaceWriter
}

// Store is a state.Store backed by a folder in an OpenCloud Space.
type Store struct {
	client  SpaceClient
	spaceID string
	prefix  string

	// mu guards space resolution and the created-directory cache. Directory
	// creation is idempotent, but caching it keeps a Put down to one round trip
	// once a folder exists.
	mu       sync.Mutex
	space    cs3.Space
	resolved bool
	dirs     map[string]struct{}
}

var _ state.Store = (*Store)(nil)

// Options configures a Store.
type Options struct {
	// SpaceID names the Space that holds service state. Required.
	SpaceID string
	// Prefix is the folder inside that Space; empty uses DefaultPrefix.
	Prefix string
}

// New constructs a Store. The Space is resolved lazily on first use, so a
// service can start before OpenCloud is reachable.
func New(client SpaceClient, opts Options) (*Store, error) {
	if client == nil {
		return nil, fmt.Errorf("cs3state: client is required")
	}
	if opts.SpaceID == "" {
		return nil, fmt.Errorf("cs3state: state space id is required")
	}
	prefix := opts.Prefix
	if prefix == "" {
		prefix = DefaultPrefix
	}
	return &Store{
		client:  client,
		spaceID: opts.SpaceID,
		prefix:  strings.Trim(prefix, "/"),
		dirs:    make(map[string]struct{}),
	}, nil
}

// Get returns the value stored under key.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	space, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}

	reader, err := s.client.OpenFile(ctx, space, s.pathFor(key), 0)
	if err != nil {
		if isNotFound(err) {
			return nil, state.ErrNotFound{Key: key}
		}
		return nil, fmt.Errorf("cs3state: read state: %w", err)
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(io.LimitReader(reader, maxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("cs3state: read state: %w", err)
	}
	if len(data) > maxDocumentBytes {
		return nil, fmt.Errorf("cs3state: state document is implausibly large")
	}
	return data, nil
}

// Create stores value under key, refusing to replace an existing document.
//
// Whether the upload itself would clobber is a property of the OpenCloud
// version underneath (pinned by TestIntegration_CS3StateOverwriteSemantics), so
// the guard does not rely on it: the parent folder is listed first, and a reva
// ALREADY_EXISTS is mapped on top of that. The listing is what makes this safe
// on a server that overwrites silently.
func (s *Store) Create(ctx context.Context, key string, value []byte) error {
	space, full, err := s.prepareWrite(ctx, key, value)
	if err != nil {
		return err
	}

	exists, err := s.exists(ctx, space, full)
	if err != nil {
		return err
	}
	if exists {
		return state.ErrExists{Key: key}
	}

	if err := s.upload(ctx, space, full, value); err != nil {
		if errors.Is(err, cs3.ErrAlreadyExists) {
			return state.ErrExists{Key: key}
		}
		return fmt.Errorf("cs3state: write state: %w", err)
	}
	return nil
}

// Replace stores value under key, discarding any previous value.
//
// The upload path may refuse to clobber (restores must never overwrite), so a
// replacement can degrade to delete-then-write. That is not atomic: a crash
// between the two loses the record. Only records the service can re-derive
// after a restart may be written this way — leases, which expire, and job
// records, which the next run rewrites. Everything whose loss is permanent is
// append-only instead (see state.Versions).
func (s *Store) Replace(ctx context.Context, key string, value []byte) error {
	space, full, err := s.prepareWrite(ctx, key, value)
	if err != nil {
		return err
	}

	err = s.upload(ctx, space, full, value)
	if errors.Is(err, cs3.ErrAlreadyExists) {
		if delErr := s.client.Delete(ctx, space, full); delErr != nil && !errors.Is(delErr, cs3.ErrNotFound) {
			return fmt.Errorf("cs3state: replace state: %w", delErr)
		}
		err = s.upload(ctx, space, full, value)
	}
	if err != nil {
		return fmt.Errorf("cs3state: write state: %w", err)
	}
	return nil
}

// prepareWrite validates a write and makes sure its folder exists.
func (s *Store) prepareWrite(ctx context.Context, key string, value []byte) (cs3.Space, string, error) {
	if len(value) > maxDocumentBytes {
		return cs3.Space{}, "", fmt.Errorf("cs3state: state document is implausibly large")
	}
	space, err := s.resolve(ctx)
	if err != nil {
		return cs3.Space{}, "", err
	}

	full := s.pathFor(key)
	if err := s.ensureDir(ctx, space, path.Dir(full)); err != nil {
		return cs3.Space{}, "", err
	}
	return space, full, nil
}

// exists reports whether a document is already stored at full.
func (s *Store) exists(ctx context.Context, space cs3.Space, full string) (bool, error) {
	entries, err := s.client.ListDir(ctx, space, path.Dir(full))
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("cs3state: read state folder: %w", err)
	}
	for _, entry := range entries {
		if strings.Trim(entry.Path, "/") == full {
			return true, nil
		}
	}
	return false, nil
}

// Delete removes key.
func (s *Store) Delete(ctx context.Context, key string) error {
	space, err := s.resolve(ctx)
	if err != nil {
		return err
	}

	if err := s.client.Delete(ctx, space, s.pathFor(key)); err != nil {
		if isNotFound(err) {
			return state.ErrNotFound{Key: key}
		}
		return fmt.Errorf("cs3state: delete state: %w", err)
	}
	return nil
}

// List returns the keys under prefix, in lexical order.
func (s *Store) List(ctx context.Context, prefix string) ([]string, error) {
	space, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}

	root := s.pathFor(prefix)
	var keys []string
	if err := s.walk(ctx, space, root, &keys); err != nil {
		return nil, err
	}
	return keys, nil
}

// walk collects the file paths below dir, relative to the store prefix.
func (s *Store) walk(ctx context.Context, space cs3.Space, dir string, out *[]string) error {
	entries, err := s.client.ListDir(ctx, space, dir)
	if err != nil {
		if isNotFound(err) {
			// A prefix nobody has written to yet is an empty listing, not a
			// failure: every store starts out that way.
			return nil
		}
		return fmt.Errorf("cs3state: list state: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir {
			if err := s.walk(ctx, space, entry.Path, out); err != nil {
				return err
			}
			continue
		}
		key, ok := s.keyFor(entry.Path)
		if !ok {
			continue
		}
		insertSorted(out, key)
	}
	return nil
}

// upload writes one document.
func (s *Store) upload(ctx context.Context, space cs3.Space, full string, value []byte) error {
	return s.client.Upload(ctx, space, full, int64(len(value)), time.Time{}, bytes.NewReader(value))
}

// ensureDir creates a folder and its parents, remembering what it made.
func (s *Store) ensureDir(ctx context.Context, space cs3.Space, dir string) error {
	dir = strings.Trim(dir, "/")
	if dir == "" || dir == "." {
		return nil
	}

	s.mu.Lock()
	_, known := s.dirs[dir]
	s.mu.Unlock()
	if known {
		return nil
	}

	if parent := path.Dir(dir); parent != "." && parent != "/" && parent != dir {
		if err := s.ensureDir(ctx, space, parent); err != nil {
			return err
		}
	}
	if err := s.client.MakeDir(ctx, space, dir); err != nil {
		return fmt.Errorf("cs3state: create state folder: %w", err)
	}

	s.mu.Lock()
	s.dirs[dir] = struct{}{}
	s.mu.Unlock()
	return nil
}

// Check resolves the state Space and refuses it if end users can reach it.
//
// A Space someone is a member of is a Space someone can empty. The records in
// here are the service's memory — including the only server-side copy of every
// wrapped Data Key — and a user who deletes a folder they do not recognise
// would be destroying backups without ever being told so. A personal Space is
// refused for the same reason plus one more: it belongs to a person, and this
// is not their document.
//
// It is called at startup so the deployment fails loudly rather than
// discovering the problem the day the folder disappears. The Space's name is
// reported to make the misconfiguration fixable; its members never are.
func (s *Store) Check(ctx context.Context) error {
	space, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	if space.Type == spaceTypePersonal {
		return fmt.Errorf("%w: %q is a personal space; service state needs a dedicated space "+
			"no end user is a member of", ErrUnsafeStateSpace, space.Name)
	}
	if len(space.Members) > 0 {
		return fmt.Errorf("%w: %q has %d member grant(s); service state needs a dedicated space "+
			"no end user is a member of", ErrUnsafeStateSpace, space.Name, len(space.Members))
	}
	return nil
}

// ErrUnsafeStateSpace reports a state Space an end user can reach. It is
// distinguishable so startup can treat it as a misconfiguration to refuse,
// rather than as the transient "OpenCloud is not up yet" it would otherwise
// look like.
var ErrUnsafeStateSpace = errors.New("cs3state: unsafe state space")

// spaceTypePersonal is the CS3 space type of a user's own Space.
const spaceTypePersonal = "personal"

// resolve finds the configured Space once and caches it.
func (s *Store) resolve(ctx context.Context) (cs3.Space, error) {
	s.mu.Lock()
	if s.resolved {
		space := s.space
		s.mu.Unlock()
		return space, nil
	}
	s.mu.Unlock()

	spaces, err := s.client.ListSpaces(ctx)
	if err != nil {
		return cs3.Space{}, fmt.Errorf("cs3state: resolve state space: %w", err)
	}
	for _, candidate := range spaces {
		if candidate.ID != s.spaceID {
			continue
		}
		s.mu.Lock()
		s.space, s.resolved = candidate, true
		s.mu.Unlock()
		return candidate, nil
	}
	// Deliberately does not name the space id: this error surfaces at startup
	// in logs, and the id is configuration, not a secret, but the message is
	// more useful pointing at the setting than at its value.
	return cs3.Space{}, errors.New("cs3state: the configured state space does not exist or is not visible to the service account")
}

// pathFor maps a state key to a space-relative path.
func (s *Store) pathFor(key string) string {
	key = strings.Trim(key, "/")
	if key == "" {
		return s.prefix
	}
	return s.prefix + "/" + key
}

// keyFor maps a space-relative path back to a state key.
func (s *Store) keyFor(full string) (string, bool) {
	full = strings.Trim(full, "/")
	if full == s.prefix {
		return "", false
	}
	rest, found := strings.CutPrefix(full, s.prefix+"/")
	if !found || rest == "" {
		return "", false
	}
	return rest, true
}

// isNotFound recognises "there is nothing there" across the CS3 client's error
// shapes.
func isNotFound(err error) bool {
	if errors.Is(err, cs3.ErrNotFound) {
		return true
	}
	// The reader reports gateway statuses as formatted errors; the not-found
	// code is the only one this store treats as an empty answer.
	return strings.Contains(err.Error(), "CODE_NOT_FOUND")
}

// insertSorted keeps the listing ordered without a sort pass per directory.
func insertSorted(out *[]string, key string) {
	list := *out
	i := len(list)
	for i > 0 && list[i-1] > key {
		i--
	}
	list = append(list, "")
	copy(list[i+1:], list[i:])
	list[i] = key
	*out = list
}
