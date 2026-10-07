package backup

// Manual runs belong to the service, not to the request that started them:
// shutdown reaches them, and a panic fails the run rather than the process
// (review-2026-10.md F7).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/snapshot"
)

// ctxBoundEngine holds a snapshot open until its context ends, like a long
// upload would.
type ctxBoundEngine struct {
	*fakeEngine
	started chan struct{}
}

func (e ctxBoundEngine) Snapshot(ctx context.Context, _ snapshot.Repo, _ snapshot.Source) (snapshot.Info, error) {
	close(e.started)
	<-ctx.Done()
	return snapshot.Info{}, ctx.Err()
}

func TestStartBackup_ShutdownCancelsTheRunAndWaitsForItsOutcome(t *testing.T) {
	bg := jobs.NewBackground(nil)
	started := make(chan struct{})
	h := newHarness(t, func(d *Deps) {
		d.Engine = ctxBoundEngine{fakeEngine: d.Engine.(*fakeEngine), started: started}
		d.Background = bg
	})

	jobID, err := h.runner.StartBackup(context.Background(), testSpaceID)
	if err != nil {
		t.Fatalf("StartBackup: %v", err)
	}
	<-started

	bg.Stop()
	bg.Wait()

	// Recorded before Wait returned: no polling needed.
	job, err := h.jobs.Get(context.Background(), jobID)
	if err != nil {
		t.Fatalf("Get job: %v", err)
	}
	if job.State != jobs.StateFailed {
		t.Fatalf("job state after shutdown = %q, want failed", job.State)
	}
	release, err := h.jobs.Acquire(context.Background(), testSpaceID)
	if err != nil {
		t.Fatalf("lock still held after shutdown: %v", err)
	}
	release()
}

func TestStartBackup_AfterShutdownIsRefusedAndRecorded(t *testing.T) {
	bg := jobs.NewBackground(nil)
	bg.Stop()
	h := newHarness(t, func(d *Deps) { d.Background = bg })

	if _, err := h.runner.StartBackup(context.Background(), testSpaceID); !errors.Is(err, jobs.ErrShuttingDown) {
		t.Fatalf("StartBackup = %v, want ErrShuttingDown", err)
	}
	all, err := h.jobs.List(context.Background(), testSpaceID)
	if err != nil || len(all) != 1 || all[0].State != jobs.StateFailed {
		t.Fatalf("jobs = %+v (%v), want one failed run", all, err)
	}
	release, err := h.jobs.Acquire(context.Background(), testSpaceID)
	if err != nil {
		t.Fatalf("lock still held: %v", err)
	}
	release()
}

// A panic in kopia or a CS3 call fails the one run, records it, gives the lock
// back, and logs the stack for the operator.
func TestRun_APanicFailsTheRunInsteadOfTheProcess(t *testing.T) {
	h := newHarness(t)
	h.engine.onSnapshot = func(snapshot.Repo) { panic("kopia exploded") }

	_, err := h.runner.RunBackup(context.Background(), testSpaceID)
	var p *jobs.PanicError
	if !errors.As(err, &p) {
		t.Fatalf("RunBackup = %v, want a PanicError", err)
	}

	all, err := h.jobs.List(context.Background(), testSpaceID)
	if err != nil || len(all) != 1 || all[0].State != jobs.StateFailed {
		t.Fatalf("jobs = %+v (%v), want one failed run", all, err)
	}
	if all[0].Error != "the backup run failed" {
		t.Fatalf("job error = %q, want the generic message", all[0].Error)
	}
	release, err := h.jobs.Acquire(context.Background(), testSpaceID)
	if err != nil {
		t.Fatalf("lock still held after a panic: %v", err)
	}
	release()

	logs := h.logs.String()
	if !strings.Contains(logs, "kopia exploded") || !strings.Contains(logs, "stack=") {
		t.Fatalf("the panic and its stack were not logged:\n%s", logs)
	}
}

func TestStartBackup_APanicFailsTheBackgroundRun(t *testing.T) {
	h := newHarness(t)
	h.engine.onSnapshot = func(snapshot.Repo) { panic("kopia exploded") }

	jobID, err := h.runner.StartBackup(context.Background(), testSpaceID)
	if err != nil {
		t.Fatalf("StartBackup: %v", err)
	}
	waitForState(t, h, jobID, jobs.StateFailed)
}
