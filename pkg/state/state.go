// Package state is the service's durable document store: opaque values filed
// under slash-separated keys, plus a typed JSON view over them.
//
// Why this exists (Phase 6): scheduling only means something if it survives a
// restart. Job history, schedules, key envelopes and target records must all
// outlive the process, and the deployment target is a single instance, so the
// store is deliberately tiny — get, put, delete, list-by-prefix.
//
// What it deliberately does NOT offer:
//
//   - Transactions or compare-and-set. The production backend is OpenCloud's own
//     storage, reached over CS3, which provides neither. Mutual exclusion between
//     concurrent runs is therefore process-local (see pkg/jobs), and durability
//     is used only to recover state after a crash. This is sound for the locked
//     single-instance deployment and unsound for a scaled-out one; anything that
//     changes that assumption needs a backend with real transactions.
//   - Queries. Callers list a prefix and filter in memory. Family scale.
//
// Everything written here is either non-secret metadata or already-wrapped
// ciphertext (SRW/TW envelopes). No plaintext key material is ever handed to a
// Store.
package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Store is the durable key/value boundary. Implementations must be safe for
// concurrent use.
//
// Keys are slash-separated paths of escaped segments (see Key). A Store may map
// them onto directories and files, so callers must not invent key syntax of
// their own — build every key with Key.
type Store interface {
	// Get returns the value stored under key, or ErrNotFound.
	Get(ctx context.Context, key string) ([]byte, error)
	// Create stores value under key. A key that already holds a value is
	// ErrExists: creating never destroys anything, which is what lets
	// append-only records (see Versions) survive a crash mid-write.
	Create(ctx context.Context, key string, value []byte) error
	// Replace stores value under key, discarding any previous value. On a
	// backend without transactions this is destructive and not atomic — the old
	// value can be gone before the new one is durable — so it is reserved for
	// records the service can re-derive after a restart (leases, job records).
	Replace(ctx context.Context, key string, value []byte) error
	// Delete removes key. Deleting a missing key returns ErrNotFound.
	Delete(ctx context.Context, key string) error
	// List returns the keys under prefix, in lexical order. The prefix is
	// matched on whole segments: "a/b" matches "a/b/c" but never "a/bc".
	List(ctx context.Context, prefix string) ([]string, error)
}

// ErrNotFound is returned when a key holds no value.
type ErrNotFound struct{ Key string }

func (e ErrNotFound) Error() string { return "state: no value for key " + e.Key }

// IsNotFound reports whether err is a not-found error from any Store.
func IsNotFound(err error) bool {
	var nf ErrNotFound
	return errors.As(err, &nf)
}

// ErrExists is returned when a create would overwrite an existing value.
type ErrExists struct{ Key string }

func (e ErrExists) Error() string { return "state: a value already exists for key " + e.Key }

// IsExists reports whether err is an already-exists error from any Store.
func IsExists(err error) bool {
	var ex ErrExists
	return errors.As(err, &ex)
}

// Key builds a key from raw segments, escaping each one. Segments come from
// untrusted-ish sources — OpenCloud space ids contain "$" and "!", and could in
// principle contain "/" — so every segment is percent-escaped down to a safe
// alphabet. The escaping is reversible (see UnescapeSegment), which is what lets
// a listing be turned back into ids.
func Key(segments ...string) string {
	out := make([]string, 0, len(segments))
	for _, s := range segments {
		out = append(out, EscapeSegment(s))
	}
	return strings.Join(out, "/")
}

// EscapeSegment percent-encodes everything outside [A-Za-z0-9._-].
//
// It is deliberately stricter than URL escaping: the CS3 backend turns segments
// into file names, and "." / ".." or a stray separator must not be able to walk
// out of the state tree.
func EscapeSegment(s string) string {
	// "." and ".." are legal file names nowhere useful and dangerous in a path,
	// so they are escaped wholesale rather than byte by byte.
	if s == "." || s == ".." {
		return strings.Repeat("%2E", len(s))
	}

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if c := s[i]; isSafeByte(c) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", s[i])
	}
	return b.String()
}

// UnescapeSegment reverses EscapeSegment.
func UnescapeSegment(s string) (string, error) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		if i+2 >= len(s) {
			return "", fmt.Errorf("state: truncated escape in %q", s)
		}
		var v int
		if _, err := fmt.Sscanf(s[i+1:i+3], "%02X", &v); err != nil {
			return "", fmt.Errorf("state: bad escape in %q", s)
		}
		b.WriteByte(byte(v))
		i += 2
	}
	return b.String(), nil
}

func isSafeByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '.' || c == '_' || c == '-':
		return true
	default:
		return false
	}
}

// Documents is a typed JSON collection stored under a fixed key prefix. It is
// the only encoder/decoder in the codebase's persistence path, so every store
// built on it (jobs, schedules, key envelopes, targets) serializes identically.
type Documents[T any] struct {
	store  Store
	prefix string
}

// NewDocuments returns a typed view over store rooted at prefix.
func NewDocuments[T any](store Store, prefix string) *Documents[T] {
	return &Documents[T]{store: store, prefix: prefix}
}

// Key returns the full key a document id maps to.
func (d *Documents[T]) Key(id ...string) string {
	return d.prefix + "/" + Key(id...)
}

// Get decodes the document at the given id path.
func (d *Documents[T]) Get(ctx context.Context, id ...string) (T, error) {
	var zero T
	raw, err := d.store.Get(ctx, d.Key(id...))
	if err != nil {
		return zero, err
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		// The value, not the error, is corrupt; never echo the bytes — a
		// document may hold wrapped key material.
		return zero, fmt.Errorf("state: decode %s: malformed document", d.prefix)
	}
	return v, nil
}

// GetKey decodes the document stored at an exact key (as returned by Keys).
func (d *Documents[T]) GetKey(ctx context.Context, key string) (T, error) {
	var zero T
	raw, err := d.store.Get(ctx, key)
	if err != nil {
		return zero, err
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return zero, fmt.Errorf("state: decode %s: malformed document", d.prefix)
	}
	return v, nil
}

// Create encodes and stores a document that must not already exist.
func (d *Documents[T]) Create(ctx context.Context, v T, id ...string) error {
	return d.CreateKey(ctx, d.Key(id...), v)
}

// CreateKey stores a document at an exact key, refusing to replace one.
func (d *Documents[T]) CreateKey(ctx context.Context, key string, v T) error {
	raw, err := d.encode(v)
	if err != nil {
		return err
	}
	return d.store.Create(ctx, key, raw)
}

// Replace encodes and stores a document, discarding any previous value. Only
// re-derivable records may use it (see Store.Replace).
func (d *Documents[T]) Replace(ctx context.Context, v T, id ...string) error {
	return d.ReplaceKey(ctx, d.Key(id...), v)
}

// ReplaceKey stores a document at an exact key, discarding any previous value.
func (d *Documents[T]) ReplaceKey(ctx context.Context, key string, v T) error {
	raw, err := d.encode(v)
	if err != nil {
		return err
	}
	return d.store.Replace(ctx, key, raw)
}

func (d *Documents[T]) encode(v T) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("state: encode %s: %w", d.prefix, err)
	}
	return raw, nil
}

// Delete removes a document.
func (d *Documents[T]) Delete(ctx context.Context, id ...string) error {
	return d.store.Delete(ctx, d.Key(id...))
}

// DeleteKey removes the document at an exact key.
func (d *Documents[T]) DeleteKey(ctx context.Context, key string) error {
	return d.store.Delete(ctx, key)
}

// Keys lists the document keys below an id path, in lexical order.
func (d *Documents[T]) Keys(ctx context.Context, id ...string) ([]string, error) {
	prefix := d.prefix
	if len(id) > 0 {
		prefix = d.Key(id...)
	}
	return d.store.List(ctx, prefix)
}

// IDs returns the distinct first-level ids the collection holds, unescaped and
// in order. A document filed under further segments reports the first one, which
// is the id its owner indexes by. It reads no documents — the ids are in the
// keys — and exists for the operations that must visit every record rather than
// one.
func (d *Documents[T]) IDs(ctx context.Context) ([]string, error) {
	keys, err := d.store.List(ctx, d.prefix)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		rest, ok := strings.CutPrefix(key, d.prefix+"/")
		if !ok {
			continue
		}
		if i := strings.Index(rest, "/"); i >= 0 {
			rest = rest[:i]
		}
		id, err := UnescapeSegment(rest)
		if err != nil || id == "" {
			continue
		}
		seen[id] = struct{}{}
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// All decodes every document below an id path, in key order. A document that
// fails to decode is skipped rather than failing the whole listing: one corrupt
// record must not make a Space's entire history unreadable.
func (d *Documents[T]) All(ctx context.Context, id ...string) ([]T, error) {
	keys, err := d.Keys(ctx, id...)
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)

	out := make([]T, 0, len(keys))
	for _, k := range keys {
		v, err := d.GetKey(ctx, k)
		if err != nil {
			if IsNotFound(err) {
				continue // deleted between listing and reading
			}
			continue // malformed; skipped deliberately
		}
		out = append(out, v)
	}
	return out, nil
}
