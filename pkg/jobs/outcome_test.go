package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/state"
)

// noSleep is the retry pause tests use: a negative backoff resolves to none.
var noSleep = RecordOptions{Backoff: -1}

// flakyStore fails the first n Finish calls, then behaves.
type flakyStore struct {
	Store

	failures int
	attempts int
	err      error
}

func (f *flakyStore) Finish(ctx context.Context, id string, out Outcome) error {
	f.attempts++
	if f.attempts <= f.failures {
		if f.err != nil {
			return f.err
		}
		return errors.New("state store unavailable")
	}
	return f.Store.Finish(ctx, id, out)
}

func newFlaky(t *testing.T, failures int) (*flakyStore, Job) {
	t.Helper()
	inner := NewStateStore(state.NewMemoryStore(), testutil.NewFakeClock(epoch))
	job, err := inner.Create(context.Background(), Job{SpaceID: "s1", Kind: KindBackup, State: StateRunning})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return &flakyStore{Store: inner, failures: failures}, job
}

// A state store that blinks costs a retry, not a job record stuck at "running"
// that nothing revisits.
func TestRecordOutcome_RetriesATransientFailure(t *testing.T) {
	store, job := newFlaky(t, 2)

	if err := RecordOutcome(context.Background(), store, job.ID, Outcome{State: StateSucceeded}, noSleep); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	if store.attempts != 3 {
		t.Fatalf("attempts = %d, want 3", store.attempts)
	}

	got, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateSucceeded {
		t.Fatalf("state = %q, want succeeded", got.State)
	}
}

// Giving up must be reported: it is the caller's signal to keep the run lock so
// the lease expires and the run is recovered.
func TestRecordOutcome_ReportsGivingUp(t *testing.T) {
	store, job := newFlaky(t, 99)

	err := RecordOutcome(context.Background(), store, job.ID, Outcome{State: StateFailed}, noSleep)
	if err == nil {
		t.Fatal("a write that never succeeded must be reported")
	}
	if store.attempts != DefaultOutcomeAttempts {
		t.Fatalf("attempts = %d, want %d", store.attempts, DefaultOutcomeAttempts)
	}
}

// Retrying an unknown job, or a non-terminal outcome, cannot help: those stop
// on the first answer rather than spending the whole backoff budget.
func TestRecordOutcome_DoesNotRetryWhatCannotSucceed(t *testing.T) {
	for name, tc := range map[string]struct {
		err error
		out Outcome
	}{
		"unknown job":  {err: ErrNotFound{ID: "gone"}, out: Outcome{State: StateSucceeded}},
		"not terminal": {err: ErrNotTerminal, out: Outcome{State: StateRunning}},
	} {
		t.Run(name, func(t *testing.T) {
			store, job := newFlaky(t, 99)
			store.err = tc.err

			if err := RecordOutcome(context.Background(), store, job.ID, tc.out, noSleep); !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if store.attempts != 1 {
				t.Fatalf("attempts = %d, want 1", store.attempts)
			}
		})
	}
}

// A run stopped by shutdown or by its own deadline must still be able to say
// so. Writing on the run's own context would fail for the very reason the run
// did, which is exactly how a Space ends up wedged.
func TestRecordOutcome_WritesOnACancelledContext(t *testing.T) {
	store, job := newFlaky(t, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := RecordOutcome(ctx, store, job.ID, Outcome{State: StateFailed, Error: "stopped"}, noSleep); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	got, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateFailed || got.Error != "stopped" {
		t.Fatalf("job = %+v", got)
	}
}

func TestRecordOutcome_RequiresAStore(t *testing.T) {
	if err := RecordOutcome(context.Background(), nil, "j", Outcome{State: StateFailed}, noSleep); err == nil {
		t.Fatal("a nil store must be refused")
	}
}

func TestRecordOptions_DefaultsAreProduction(t *testing.T) {
	attempts, timeout, backoff, sleep := RecordOptions{}.resolve()
	if attempts != DefaultOutcomeAttempts || timeout != DefaultOutcomeTimeout || backoff != DefaultOutcomeBackoff {
		t.Fatalf("defaults = %d, %v, %v", attempts, timeout, backoff)
	}
	if sleep == nil {
		t.Fatal("sleep must default to a real wait")
	}
}

func TestSleepFor_ReturnsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		sleepFor(ctx, time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sleepFor ignored a cancelled context")
	}
}
