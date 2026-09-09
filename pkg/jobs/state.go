package jobs

// The durable Store, on top of pkg/state.
//
// Layout: one document per run, at
//
//	jobs/<space-id>/<created-unix-nanos>-<kind>-<job-id>
//
// Three properties fall out of that key and are what make this cheap enough for
// a store with no queries: a Space's history is a prefix listing; because the
// timestamp is fixed-width and leading, lexical key order *is* chronological
// order; and the kind is in the name, so "the last backup" can skip every
// restore and prune without reading them. Serving "the last five runs"
// therefore reads five documents, not the Space's whole history, and pruning
// decides what to delete from key names alone.
//
// Keys written before the kind was part of the name (<nanos>-<job-id>) are
// still read: their kind is simply unknown until the document is fetched, so a
// kind-filtered listing reads them and filters afterwards. That cost fades on
// its own as history rolls over.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"opencloud-backup-plugin/pkg/state"
)

// jobsPrefix roots every job document.
const jobsPrefix = "jobs"

// keyTimeWidth pads the nanosecond timestamp so keys sort lexically. Unix nanos
// need 19 digits until the year 2262.
const keyTimeWidth = 19

// StateStore is a Store backed by durable state.
type StateStore struct {
	docs  *state.Documents[Job]
	clock Clock

	// mu guards the id index and the terminal set. The index maps job id ->
	// full document key, so a job can be found by id without knowing its Space;
	// terminal holds the keys already known to hold a finished run, so a
	// repeated ListRunning re-reads only what it has not seen.
	mu       sync.Mutex
	index    map[string]string
	indexed  bool
	terminal map[string]struct{}
}

var _ Store = (*StateStore)(nil)

// NewStateStore returns a Store persisting to st.
func NewStateStore(st state.Store, clock Clock) *StateStore {
	if clock == nil {
		clock = systemClock{}
	}
	return &StateStore{
		docs:     state.NewDocuments[Job](st, jobsPrefix),
		clock:    clock,
		index:    make(map[string]string),
		terminal: make(map[string]struct{}),
	}
}

// Create records a new job.
func (s *StateStore) Create(ctx context.Context, j Job) (Job, error) {
	j, err := prepare(j, s.clock.Now())
	if err != nil {
		return Job{}, err
	}

	key := s.docs.Key(j.SpaceID, documentName(j.CreatedAt, j.Kind, j.ID))
	if err := s.docs.CreateKey(ctx, key, j); err != nil {
		return Job{}, fmt.Errorf("jobs: store job: %w", err)
	}

	s.mu.Lock()
	s.index[j.ID] = key
	s.mu.Unlock()
	return j, nil
}

// Get returns one job by id.
func (s *StateStore) Get(ctx context.Context, id string) (Job, error) {
	key, err := s.keyFor(ctx, id)
	if err != nil {
		return Job{}, err
	}
	j, err := s.docs.GetKey(ctx, key)
	if err != nil {
		if state.IsNotFound(err) {
			s.forget(id)
			return Job{}, ErrNotFound{ID: id}
		}
		return Job{}, fmt.Errorf("jobs: read job: %w", err)
	}
	return j, nil
}

// List returns a Space's jobs, newest first.
func (s *StateStore) List(ctx context.Context, spaceID string) ([]Job, error) {
	return s.ListRecent(ctx, spaceID, 0)
}

// ListRecent returns at most limit of a Space's jobs, newest first. Only the
// documents actually returned are read.
func (s *StateStore) ListRecent(ctx context.Context, spaceID string, limit int) ([]Job, error) {
	return s.listRecent(ctx, spaceID, "", limit)
}

// ListRecentOfKind returns at most limit of a Space's jobs of one kind, newest
// first. Documents whose key already says they are of another kind are not
// read, so asking for one backup costs one read however many restores and
// prunes happened since.
func (s *StateStore) ListRecentOfKind(ctx context.Context, spaceID string, kind Kind, limit int) ([]Job, error) {
	if kind == "" {
		return nil, fmt.Errorf("jobs: kind required")
	}
	return s.listRecent(ctx, spaceID, kind, limit)
}

// listRecent walks a Space's history newest-first, reading only the documents
// it returns. An empty kind matches every kind.
func (s *StateStore) listRecent(ctx context.Context, spaceID string, kind Kind, limit int) ([]Job, error) {
	keys, err := s.docs.Keys(ctx, spaceID)
	if err != nil {
		return nil, fmt.Errorf("jobs: list history: %w", err)
	}

	capacity := len(keys)
	if limit > 0 {
		capacity = min(capacity, limit)
	}

	out := make([]Job, 0, capacity)
	// Keys are chronological, so walking backwards is newest-first.
	for i := len(keys) - 1; i >= 0; i-- {
		if limit > 0 && len(out) == limit {
			break
		}
		key := keys[i]
		// A key that names a kind answers the filter without a read. One that
		// does not (written before the kind was in the name) has to be read.
		if k, named := kindFromKey(key); kind != "" && named && k != kind {
			continue
		}
		j, err := s.docs.GetKey(ctx, key)
		if err != nil {
			// A record deleted or corrupted underneath us must not break the
			// whole listing.
			continue
		}
		s.remember(j.ID, key)
		if kind != "" && j.Kind != kind {
			continue
		}
		out = append(out, j)
	}
	sortNewestFirst(out)
	return out, nil
}

// Finish records a terminal state and the run's outcome in one write.
func (s *StateStore) Finish(ctx context.Context, id string, out Outcome) error {
	if !out.State.Terminal() {
		return ErrNotTerminal
	}

	key, err := s.keyFor(ctx, id)
	if err != nil {
		return err
	}
	j, err := s.docs.GetKey(ctx, key)
	if err != nil {
		if state.IsNotFound(err) {
			s.forget(id)
			return ErrNotFound{ID: id}
		}
		return fmt.Errorf("jobs: read job: %w", err)
	}
	// A job record is re-derivable state: it is written again on every state
	// change and its loss costs a history entry, not a backup. Replacing it in
	// place is therefore allowed where a key envelope's would not be.
	if err := s.docs.ReplaceKey(ctx, key, applyOutcome(j, out, s.clock.Now())); err != nil {
		return fmt.Errorf("jobs: store job: %w", err)
	}
	s.markSettled(key)
	return nil
}

// PruneBefore deletes finished jobs created before cutoff. Candidates are
// picked by key (no read), and only a candidate's document is read — to confirm
// it is not still running before it is dropped.
func (s *StateStore) PruneBefore(ctx context.Context, cutoff time.Time) (int, error) {
	keys, err := s.docs.Keys(ctx)
	if err != nil {
		return 0, fmt.Errorf("jobs: list history: %w", err)
	}

	removed := 0
	for _, key := range keys {
		created, ok := createdFromKey(key)
		if !ok || !created.Before(cutoff) {
			continue
		}
		j, err := s.docs.GetKey(ctx, key)
		if err != nil || !prunable(j, cutoff) {
			continue
		}
		if err := s.docs.DeleteKey(ctx, key); err != nil && !state.IsNotFound(err) {
			return removed, fmt.Errorf("jobs: prune history: %w", err)
		}
		s.forget(j.ID, key)
		removed++
	}
	return removed, nil
}

// ListRunning returns every job, in any Space, that never reached a terminal
// state.
//
// Nothing in the key says whether a run finished, so answering this means
// reading documents. What keeps that affordable is that the answer, once given,
// never changes: a terminal job stays terminal. Keys already seen terminal are
// therefore remembered and never read again, so the first sweep of a process
// reads the history once and every later sweep reads only what is new.
func (s *StateStore) ListRunning(ctx context.Context) ([]Job, error) {
	keys, err := s.docs.Keys(ctx)
	if err != nil {
		return nil, fmt.Errorf("jobs: list history: %w", err)
	}

	var out []Job
	for _, key := range keys {
		if s.settled(key) {
			continue
		}
		j, err := s.docs.GetKey(ctx, key)
		if err != nil {
			// A record deleted or corrupted underneath us must not hide the
			// running jobs this exists to find.
			continue
		}
		if j.State.Terminal() {
			s.markSettled(key)
			continue
		}
		out = append(out, j)
	}
	sortNewestFirst(out)
	return out, nil
}

// settled reports whether a key has already been read and found terminal.
func (s *StateStore) settled(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.terminal[key]
	return ok
}

// markSettled remembers that a key holds a finished run.
func (s *StateStore) markSettled(key string) {
	s.mu.Lock()
	s.terminal[key] = struct{}{}
	s.mu.Unlock()
}

// keyFor resolves a job id to its document key, building the index on first
// miss. The index is derived from key names only, so hydrating it reads no
// documents.
func (s *StateStore) keyFor(ctx context.Context, id string) (string, error) {
	s.mu.Lock()
	key, ok := s.index[id]
	indexed := s.indexed
	s.mu.Unlock()
	if ok {
		return key, nil
	}
	if indexed {
		return "", ErrNotFound{ID: id}
	}

	keys, err := s.docs.Keys(ctx)
	if err != nil {
		return "", fmt.Errorf("jobs: index jobs: %w", err)
	}

	s.mu.Lock()
	for _, k := range keys {
		if jobID, ok := idFromKey(k); ok {
			s.index[jobID] = k
		}
	}
	s.indexed = true
	key, ok = s.index[id]
	s.mu.Unlock()

	if !ok {
		return "", ErrNotFound{ID: id}
	}
	return key, nil
}

func (s *StateStore) remember(id, key string) {
	s.mu.Lock()
	s.index[id] = key
	s.mu.Unlock()
}

// forget drops a job from both caches. The key is optional: a lookup that found
// nothing knows the id but not where it would have lived.
func (s *StateStore) forget(id string, key ...string) {
	s.mu.Lock()
	delete(s.index, id)
	for _, k := range key {
		delete(s.terminal, k)
	}
	s.mu.Unlock()
}

// documentName is the per-job key segment: a fixed-width timestamp so lexical
// order is chronological, then the kind so a listing can filter without
// reading, then the id so the name is unique.
func documentName(created time.Time, kind Kind, id string) string {
	return fmt.Sprintf("%0*d-%s-%s", keyTimeWidth, created.UTC().UnixNano(), kind, id)
}

// idFromKey extracts a job id from a document key, in either layout.
func idFromKey(key string) (string, bool) {
	_, rest, ok := strings.Cut(documentSegment(key), "-")
	if !ok || rest == "" {
		return "", false
	}
	// A kind segment, when present, sits between the timestamp and the id. A
	// job id is hex, so it can never be mistaken for a kind.
	if kind, id, ok := strings.Cut(rest, "-"); ok && id != "" && isKind(kind) {
		return id, true
	}
	return rest, true
}

// kindFromKey reports the kind named in a document key. A key written before
// the kind was part of the name names none, which is not an error: the caller
// falls back to reading the document.
func kindFromKey(key string) (Kind, bool) {
	_, rest, ok := strings.Cut(documentSegment(key), "-")
	if !ok {
		return "", false
	}
	kind, id, ok := strings.Cut(rest, "-")
	if !ok || id == "" || !isKind(kind) {
		return "", false
	}
	return Kind(kind), true
}

// documentSegment is the last path segment of a document key: the job's name.
func documentSegment(key string) string {
	return key[strings.LastIndex(key, "/")+1:]
}

// isKind reports whether s names a kind this build knows. An unknown segment is
// treated as part of an id rather than as a kind, so a key written by a future
// version is misread as legacy — which costs a document read, not correctness.
func isKind(s string) bool {
	switch Kind(s) {
	case KindBackup, KindPrune, KindRestore:
		return true
	default:
		return false
	}
}

// createdFromKey extracts a job's creation time from a document key.
func createdFromKey(key string) (time.Time, bool) {
	stamp, _, ok := strings.Cut(documentSegment(key), "-")
	if !ok {
		return time.Time{}, false
	}
	nanos, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, nanos).UTC(), true
}
