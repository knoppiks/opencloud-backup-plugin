package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestOutcomeWorstCaseCoversEveryAttempt(t *testing.T) {
	want := 3*10*time.Second + 2*2*time.Second
	if OutcomeWorstCase != want {
		t.Fatalf("OutcomeWorstCase = %v, want %v", OutcomeWorstCase, want)
	}
}

func TestBackgroundStopCancelsAndWaitDrains(t *testing.T) {
	bg := NewBackground(nil)
	finished := make(chan struct{})
	started := make(chan struct{})
	if err := bg.Go("run", func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		time.Sleep(10 * time.Millisecond) // records its outcome
		close(finished)
	}); err != nil {
		t.Fatalf("Go: %v", err)
	}
	<-started

	bg.Stop()
	bg.Wait()
	select {
	case <-finished:
	default:
		t.Fatal("Wait returned before the task finished")
	}
}

func TestBackgroundRefusesWorkAfterStop(t *testing.T) {
	bg := NewBackground(nil)
	bg.Stop()
	ran := false
	if err := bg.Go("late", func(context.Context) { ran = true }); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Go after Stop = %v, want ErrShuttingDown", err)
	}
	bg.Wait()
	if ran {
		t.Fatal("work offered after Stop ran")
	}
}

func TestBackgroundContainsAPanic(t *testing.T) {
	var logs bytes.Buffer
	bg := NewBackground(slog.New(slog.NewTextHandler(&logs, nil)))
	if err := bg.Go("explodes", func(context.Context) { panic("boom") }); err != nil {
		t.Fatalf("Go: %v", err)
	}
	bg.Wait()
	if !strings.Contains(logs.String(), "background task panicked") || !strings.Contains(logs.String(), "boom") {
		t.Fatalf("panic not logged:\n%s", logs.String())
	}
}

func TestRecoverTurnsAPanicIntoAnError(t *testing.T) {
	got, err := Recover(func() (int, error) { panic("kopia") })
	var p *PanicError
	if !errors.As(err, &p) || p.Value != "kopia" || got != 0 {
		t.Fatalf("Recover = %d, %v; want a PanicError", got, err)
	}
	if PanicStack(err) == "" {
		t.Fatal("no stack recorded")
	}
	wrapped := errors.Join(errors.New("context"), err)
	if PanicStack(wrapped) == "" {
		t.Fatal("stack not found through wrapping")
	}

	got, err = Recover(func() (int, error) { return 7, nil })
	if got != 7 || err != nil || PanicStack(err) != "" {
		t.Fatalf("Recover = %d, %v; want the value through", got, err)
	}
}
