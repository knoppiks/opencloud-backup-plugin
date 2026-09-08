package jobs

// The durable Store, on top of pkg/state.
//
// Layout: one document per run, at
//
//	jobs/<space-id>/<created-unix-nanos>-<job-id>
//
// Two properties fall out of that key and are what make this cheap enough for a
// store with no queries: a Space's history is a prefix listing, and because the
// timestamp is fixed-width and leading, lexical key order *is* chronological
// order. Serving "the last five runs" therefore reads five documents, not the
// Space's whole history, and pruning decides what to delete from key names
// alone.

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

	key := s.docs.Key(j.SpaceID, documentName(j.CreatedAt, j.ID))
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
	keys, err := s.docs.Keys(ctx, spaceID)
	if err != nil {
		return nil, fmt.Errorf("jobs: list history: %w", err)
	}
	// Keys are chronological; take the newest tail.
	if limit > 0 && len(keys) > limit {
		keys = keys[len(keys)-limit:]
	}

	out := make([]Job, 0, len(keys))
	for _, key := range keys {
		j, err := s.docs.GetKey(ctx, key)
		if err != nil {
			// A record deleted or corrupted underneath us must not break the
			// whole listing.
			continue
		}
		out = append(out, j)
		s.remember(j.ID, key)
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
// order is chronological, then the id so the name is unique.
func documentName(created time.Time, id string) string {
	return fmt.Sprintf("%0*d-%s", keyTimeWidth, created.UTC().UnixNano(), id)
}

// idFromKey extracts a job id from a document key.
func idFromKey(key string) (string, bool) {
	name := key[strings.LastIndex(key, "/")+1:]
	sep := strings.Index(name, "-")
	if sep < 0 || sep+1 >= len(name) {
		return "", false
	}
	return name[sep+1:], true
}

// createdFromKey extracts a job's creation time from a document key.
func createdFromKey(key string) (time.Time, bool) {
	name := key[strings.LastIndex(key, "/")+1:]
	sep := strings.Index(name, "-")
	if sep < 0 {
		return time.Time{}, false
	}
	nanos, err := strconv.ParseInt(name[:sep], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, nanos).UTC(), true
}
