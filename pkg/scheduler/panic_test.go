package scheduler

// A panic anywhere in the scheduler's goroutines must cost at most the work it
// happened in, never the process (review-2026-10.md F7).

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/spacecfg"
)

// panickyRunner panics on every run of one Space and delegates the rest.
type panickyRunner struct {
	*fakeRunner
	space  string
	panics atomic.Int32
}

func (p *panickyRunner) RunScheduled(ctx context.Context, spaceID string) error {
	if spaceID == p.space {
		p.panics.Add(1)
		panic("kopia exploded")
	}
	return p.fakeRunner.RunScheduled(ctx, spaceID)
}

func TestDispatch_APanickingRunFreesItsSlotAndSpace(t *testing.T) {
	var runner *panickyRunner
	h := newHarness(t, Options{Jitter: -1, MaxConcurrent: 1}, func(d *Deps) {
		runner = &panickyRunner{fakeRunner: d.Runner.(*fakeRunner), space: "boom"}
		d.Runner = runner
	})
	h.configure("boom", "* * * * *")

	h.clock.Advance(2 * time.Minute)
	h.tick()
	if runner.panics.Load() != 1 {
		t.Fatalf("panics = %d, want the run to have been attempted", runner.panics.Load())
	}
	if logs := h.logs.String(); !strings.Contains(logs, "scheduled run panicked") ||
		!strings.Contains(logs, "kopia exploded") {
		t.Fatalf("the panic was not logged:\n%s", logs)
	}

	// The only slot and the Space are free again: the panicking Space is tried
	// again on a later tick rather than counted as forever in flight, and once
	// it is gone another Space gets the slot.
	h.clock.Advance(2 * time.Minute)
	h.tick()
	if runner.panics.Load() != 2 {
		t.Fatalf("panics = %d, want the panicking space retried", runner.panics.Load())
	}

	if err := h.configs.Delete(context.Background(), "boom"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	h.configure("fine", "* * * * *")
	h.clock.Advance(2 * time.Minute)
	h.tick()
	if got := h.runner.calls(); len(got) != 1 || got[0] != "fine" {
		t.Fatalf("runs = %v, want the other space to have the slot", got)
	}
}

// panicOnceConfigs panics on its first List, as a bug in a tick would.
type panicOnceConfigs struct {
	spacecfg.Store
	calls atomic.Int32
}

func (p *panicOnceConfigs) List(ctx context.Context) ([]spacecfg.Config, []string, error) {
	if p.calls.Add(1) == 1 {
		panic("tick bug")
	}
	return p.Store.List(ctx)
}

func TestRun_SurvivesAPanickingTick(t *testing.T) {
	clock := testutil.NewFakeClock(epoch)
	configs := &panicOnceConfigs{Store: spacecfg.NewMemoryStoreWithClock(clock)}
	jobStore := jobs.NewMemoryStoreWithClock(clock)
	runner := &fakeRunner{store: jobStore, started: make(chan string, 1)}

	sched, err := New(Deps{Configs: configs, Jobs: jobStore, Runner: runner, Clock: clock},
		Options{Interval: time.Millisecond, Jitter: -1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := configs.Put(context.Background(), spacecfg.Config{
		SpaceID: "s1", TargetID: "t1", Schedule: "30 2 * * *", Enabled: true,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	clock.Set(epoch.Add(3 * time.Hour))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx) }()

	select {
	case <-runner.started:
	case err := <-done:
		t.Fatalf("Run returned %v after a panicking tick", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no run after a panicking tick")
	}
	if configs.calls.Load() < 2 {
		t.Fatalf("List calls = %d, want the loop to have ticked again", configs.calls.Load())
	}
}
