package state

// Append-only records, for the state whose loss is not recoverable.
//
// The backend has no transactions and no compare-and-set (see the package
// comment), so replacing a document in place is destructive: the old value has
// to go before the new one is durable, and a crash inside that window loses the
// record. For a job record that is a cosmetic loss — the next run rewrites it.
// For a Space's wrapped Data Key it is permanent: no envelope, no restore, ever.
//
// Versions removes that window for exactly those records. A write never touches
// what is already stored; it adds a document whose name is a fixed-width
// timestamp, and a read takes the newest name. A crash therefore leaves the
// previous version intact and readable, and the cost is one extra listing per
// read.
//
// Nothing is deleted here except by an explicit caller (an admin removing a
// target). Superseded versions are tiny, are ciphertext where they hold key
// material at all, and are the only audit trail rotation has.

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// versionWidth pads the nanosecond timestamp in a version name so that lexical
// order is chronological order. Unix nanos need 19 digits until the year 2262.
const versionWidth = 19

// appendAttempts bounds the search for a free version name. Names collide only
// when two writes share a nanosecond (or a test clock stands still), and each
// attempt moves one nanosecond forward, so this is generous.
const appendAttempts = 32

// Versions is an append-only, typed collection of documents filed under
// "<prefix>/<id...>/<nanos>". Writes create a new version; reads take the
// newest.
type Versions[T any] struct {
	docs *Documents[T]
	// legacy is an optional pre-versioned layout: one document per id, at
	// "<legacy-prefix>/<id...>". It is read when an id has no versions yet and
	// is never written to or deleted by an append, so a deployment that
	// predates versioning keeps working and keeps its original record.
	legacy *Documents[T]
}

// NewVersions returns an append-only collection rooted at prefix.
func NewVersions[T any](store Store, prefix string) *Versions[T] {
	return &Versions[T]{docs: NewDocuments[T](store, prefix)}
}

// WithLegacy declares the single-document prefix this collection replaces, so
// records written before versioning existed are still readable. It returns the
// receiver for chaining.
func (v *Versions[T]) WithLegacy(prefix string) *Versions[T] {
	v.legacy = NewDocuments[T](v.docs.store, prefix)
	return v
}

// Append stores a new version of a record. at is the version's timestamp; it is
// moved forward when it would collide with, or sort before, the newest existing
// version, because a clock that jumps backwards must not be able to make a new
// record look older than the one it supersedes.
func (v *Versions[T]) Append(ctx context.Context, at time.Time, value T, id ...string) error {
	prefix := v.docs.Key(id...)
	keys, err := v.docs.store.List(ctx, prefix)
	if err != nil {
		return err
	}

	nanos := at.UTC().UnixNano()
	if newest, _, ok := newestVersion(keys); ok && newest >= nanos {
		nanos = newest + 1
	}

	for attempt := 0; attempt < appendAttempts; attempt++ {
		err := v.docs.CreateKey(ctx, prefix+"/"+versionName(nanos), value)
		if err == nil {
			return nil
		}
		if !IsExists(err) {
			return err
		}
		nanos++
	}
	return fmt.Errorf("state: could not find a free version name under %s", v.docs.prefix)
}

// Span is when a record's stored versions were written. Both fields are the
// zero value for a record that has no versions — one read from the legacy
// layout, which carries no version stamps.
type Span struct {
	// Oldest is the write time of the oldest version still stored.
	Oldest time.Time
	// Newest is the write time of the newest version.
	Newest time.Time
}

// Newest returns the newest version of a record, falling back to the legacy
// single-document layout, or ErrNotFound.
func (v *Versions[T]) Newest(ctx context.Context, id ...string) (T, error) {
	value, _, err := v.Load(ctx, id...)
	return value, err
}

// Keys returns the version keys stored for a record, oldest first. It reads no
// documents: the names carry the timestamps.
func (v *Versions[T]) Keys(ctx context.Context, id ...string) ([]string, error) {
	keys, err := v.docs.store.List(ctx, v.docs.Key(id...))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := versionNanos(key); ok {
			out = append(out, key)
		}
	}
	return out, nil
}

// Load returns the newest version of a record together with the span of
// versions stored for it, in one listing.
func (v *Versions[T]) Load(ctx context.Context, id ...string) (T, Span, error) {
	var zero T

	prefix := v.docs.Key(id...)
	keys, err := v.docs.store.List(ctx, prefix)
	if err != nil {
		return zero, Span{}, err
	}
	if nanos, key, ok := newestVersion(keys); ok {
		value, err := v.docs.GetKey(ctx, key)
		if err != nil {
			return zero, Span{}, err
		}
		oldest, _, _ := versionSpan(keys)
		return value, Span{Oldest: time.Unix(0, oldest).UTC(), Newest: time.Unix(0, nanos).UTC()}, nil
	}

	if v.legacy == nil {
		return zero, Span{}, ErrNotFound{Key: prefix}
	}
	value, err := v.legacy.Get(ctx, id...)
	if err != nil {
		return zero, Span{}, err
	}
	return value, Span{}, nil
}

// Latest returns the newest version of every record in the collection, in id
// order, including records that exist only in the legacy layout.
func (v *Versions[T]) Latest(ctx context.Context) ([]T, error) {
	keys, err := v.docs.store.List(ctx, v.docs.prefix)
	if err != nil {
		return nil, err
	}

	newestByID := make(map[string]string, len(keys))
	newestNanos := make(map[string]int64, len(keys))
	for _, key := range keys {
		nanos, ok := versionNanos(key)
		if !ok {
			continue
		}
		id := path.Dir(key)
		if existing, seen := newestNanos[id]; seen && existing >= nanos {
			continue
		}
		newestByID[id], newestNanos[id] = key, nanos
	}

	ids := make([]string, 0, len(newestByID))
	for id := range newestByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]T, 0, len(ids)+1)
	for _, id := range ids {
		value, err := v.docs.GetKey(ctx, newestByID[id])
		if err != nil {
			// A record removed or corrupted underneath the listing must not
			// make the whole collection unreadable.
			continue
		}
		out = append(out, value)
	}
	return v.appendLegacy(ctx, out, newestByID)
}

// appendLegacy adds records that exist only in the pre-versioned layout.
func (v *Versions[T]) appendLegacy(ctx context.Context, out []T, versioned map[string]string) ([]T, error) {
	if v.legacy == nil {
		return out, nil
	}
	keys, err := v.legacy.Keys(ctx)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		id, ok := strings.CutPrefix(key, v.legacy.prefix+"/")
		if !ok {
			continue
		}
		if _, superseded := versioned[v.docs.prefix+"/"+id]; superseded {
			continue
		}
		value, err := v.legacy.GetKey(ctx, key)
		if err != nil {
			continue
		}
		out = append(out, value)
	}
	return out, nil
}

// DeleteAll removes every version of a record, and its legacy document. It is
// the only destructive operation in this file and exists for records a user or
// admin may genuinely retire (a backup target, a Space's configuration). It
// reports whether anything was removed.
func (v *Versions[T]) DeleteAll(ctx context.Context, id ...string) (bool, error) {
	prefix := v.docs.Key(id...)
	keys, err := v.docs.store.List(ctx, prefix)
	if err != nil {
		return false, err
	}

	removed := false
	for _, key := range keys {
		if err := v.docs.DeleteKey(ctx, key); err != nil {
			if IsNotFound(err) {
				continue
			}
			return removed, err
		}
		removed = true
	}

	if v.legacy != nil {
		switch err := v.legacy.Delete(ctx, id...); {
		case err == nil:
			removed = true
		case IsNotFound(err):
		default:
			return removed, err
		}
	}
	return removed, nil
}

// NewestKey returns the lexically last key under prefix, or ErrNotFound when the
// prefix holds nothing. It is the primitive Versions is built on, exported
// because "read the newest record under this prefix" is the shape every
// append-only store needs.
func NewestKey(ctx context.Context, store Store, prefix string) (string, error) {
	keys, err := store.List(ctx, prefix)
	if err != nil {
		return "", err
	}
	if _, key, ok := newestVersion(keys); ok {
		return key, nil
	}
	return "", ErrNotFound{Key: prefix}
}

// versionName renders a version's timestamp as a fixed-width, sortable name.
func versionName(nanos int64) string {
	return fmt.Sprintf("%0*d", versionWidth, nanos)
}

// versionNanos reads the timestamp out of a version key.
func versionNanos(key string) (int64, bool) {
	name := key[strings.LastIndex(key, "/")+1:]
	if len(name) != versionWidth {
		return 0, false
	}
	nanos, err := strconv.ParseInt(name, 10, 64)
	if err != nil || nanos < 0 {
		return 0, false
	}
	return nanos, true
}

// newestVersion picks the highest-stamped key of a listing.
func newestVersion(keys []string) (int64, string, bool) {
	var (
		best  int64
		key   string
		found bool
	)
	for _, candidate := range keys {
		nanos, ok := versionNanos(candidate)
		if !ok || (found && nanos <= best) {
			continue
		}
		best, key, found = nanos, candidate, true
	}
	return best, key, found
}

// versionSpan reports the oldest and newest timestamps in a listing.
func versionSpan(keys []string) (oldest, newest int64, ok bool) {
	for _, key := range keys {
		nanos, valid := versionNanos(key)
		if !valid {
			continue
		}
		if !ok || nanos < oldest {
			oldest = nanos
		}
		if !ok || nanos > newest {
			newest = nanos
		}
		ok = true
	}
	return oldest, newest, ok
}
