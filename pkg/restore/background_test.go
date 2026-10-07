package restore

// Background restores belong to the service, not to the request that started
// them; a panic fails the run, not the process (review-2026-10.md F7).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/snapshot"
)

// ctxBoundEngine holds a restore open until its context ends.
type ctxBoundEngine struct {
	*fakeEngine
	started chan struct{}
}

func (e ctxBoundEngine) Walk(ctx context.Context, _ snapshot.Repo, _ snapshot.SnapshotID,
	_ func(context.Context, snapshot.RestoredEntry) error,
) error {
	close(e.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestStartRestore_ShutdownCancelsTheRunAndWaitsForItsOutcome(t *testing.T) {
	h := newHarness(t)
	bg := jobs.NewBackground(nil)
	started := make(chan struct{})
	h.runner.deps.Background = bg
	h.runner.deps.Engine = ctxBoundEngine{fakeEngine: h.engine, started: started}

	jobID, err := h.runner.StartRestore(context.Background(), testSpaceID, testSnapshot)
	if err != nil {
		t.Fatalf("StartRestore: %v", err)
	}
	<-started

	bg.Stop()
	bg.Wait()

	job, err := h.jobs.Get(context.Background(), jobID)
	if err != nil || job.State != jobs.StateFailed {
		t.Fatalf("job after shutdown = %+v (%v), want failed", job, err)
	}
	release, err := h.jobs.Acquire(context.Background(), testSpaceID)
	if err != nil {
		t.Fatalf("lock still held after shutdown: %v", err)
	}
	release()
}

func TestStartRestore_AfterShutdownIsRefused(t *testing.T) {
	h := newHarness(t)
	bg := jobs.NewBackground(nil)
	bg.Stop()
	h.runner.deps.Background = bg

	if _, err := h.runner.StartRestore(context.Background(), testSpaceID, testSnapshot); !errors.Is(err, jobs.ErrShuttingDown) {
		t.Fatalf("StartRestore = %v, want ErrShuttingDown", err)
	}
	release, err := h.jobs.Acquire(context.Background(), testSpaceID)
	if err != nil {
		t.Fatalf("lock still held: %v", err)
	}
	release()
}

func TestRunRestore_APanicFailsTheRunInsteadOfTheProcess(t *testing.T) {
	h := newHarness(t)
	h.engine.onWalk = func(snapshot.Repo) { panic("kopia exploded") }

	_, err := h.runner.RunRestore(context.Background(), testSpaceID, testSnapshot)
	var p *jobs.PanicError
	if !errors.As(err, &p) {
		t.Fatalf("RunRestore = %v, want a PanicError", err)
	}
	all, err := h.jobs.List(context.Background(), testSpaceID)
	if err != nil || len(all) != 1 || all[0].State != jobs.StateFailed {
		t.Fatalf("jobs = %+v (%v), want one failed run", all, err)
	}
	release, err := h.jobs.Acquire(context.Background(), testSpaceID)
	if err != nil {
		t.Fatalf("lock still held after a panic: %v", err)
	}
	release()
	if logs := h.logs.String(); !strings.Contains(logs, "kopia exploded") || !strings.Contains(logs, "stack=") {
		t.Fatalf("the panic and its stack were not logged:\n%s", logs)
	}
}
