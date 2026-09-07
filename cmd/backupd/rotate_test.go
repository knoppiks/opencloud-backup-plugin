package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/state"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestRunCommandRejectsUnknownCommands(t *testing.T) {
	err := runCommand(context.Background(), "rotate-everything", nil, discardLogger())
	if err == nil {
		t.Fatal("an unknown command was accepted")
	}
	if !strings.Contains(err.Error(), "rotate-srw") {
		t.Fatalf("err = %v, want the known commands listed", err)
	}
}

// The operator's assertion is required before anything is read, let alone
// written: a rotation racing a backup run hands that run an envelope it cannot
// open.
func TestRotateRefusesWithoutTheServiceStoppedFlag(t *testing.T) {
	for _, command := range []string{"rotate-srw", "rotate-tw"} {
		t.Run(command, func(t *testing.T) {
			// Deliberately configured: the flag must be checked first, so this
			// fails on the flag and not on missing keys.
			t.Setenv("SRW_KEY_OLD", encodedKey(t))
			t.Setenv("SRW_KEY", encodedKey(t))
			t.Setenv("TW_KEY_OLD", encodedKey(t))
			t.Setenv("TW_KEY", encodedKey(t))

			err := runCommand(context.Background(), command, nil, discardLogger())
			if err == nil {
				t.Fatal("rotation ran without -service-stopped")
			}
			if !strings.Contains(err.Error(), "-service-stopped") {
				t.Fatalf("err = %v, want it to say what to do", err)
			}
		})
	}
}

func TestRotateRequiresBothKeys(t *testing.T) {
	cases := map[string]struct{ old, new string }{
		"neither key": {},
		"only old":    {old: encodedKey(t)},
		"only new":    {new: encodedKey(t)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SRW_KEY_OLD", c.old)
			t.Setenv("SRW_KEY", c.new)

			err := runCommand(context.Background(), "rotate-srw", []string{"-service-stopped"}, discardLogger())
			if err == nil {
				t.Fatal("rotation ran without both keys")
			}
			if !strings.Contains(err.Error(), "required") {
				t.Fatalf("err = %v, want a missing-configuration error", err)
			}
		})
	}
}

// Without a state Space there is nothing durable to rotate; falling back to
// in-memory state here would report a cheerful success having done nothing.
func TestRotateRequiresDurableState(t *testing.T) {
	t.Setenv("SRW_KEY_OLD", encodedKey(t))
	t.Setenv("SRW_KEY", encodedKey(t))
	t.Setenv("CS3_GATEWAY_ADDR", "127.0.0.1:0")
	t.Setenv("STATE_SPACE_ID", "")

	err := runCommand(context.Background(), "rotate-srw", []string{"-service-stopped"}, discardLogger())
	if err == nil {
		t.Fatal("rotation ran against in-memory state")
	}
	if !strings.Contains(err.Error(), "STATE_SPACE_ID") {
		t.Fatalf("err = %v, want it to name the missing configuration", err)
	}
}

func TestRotateRequiresACS3Gateway(t *testing.T) {
	t.Setenv("SRW_KEY_OLD", encodedKey(t))
	t.Setenv("SRW_KEY", encodedKey(t))
	t.Setenv("CS3_GATEWAY_ADDR", "")

	err := runCommand(context.Background(), "rotate-srw", []string{"-service-stopped"}, discardLogger())
	if err == nil {
		t.Fatal("rotation ran without a gateway")
	}
	if !strings.Contains(err.Error(), "CS3_GATEWAY_ADDR") {
		t.Fatalf("err = %v, want it to name the missing configuration", err)
	}
}

func TestRotateRejectsStrayArguments(t *testing.T) {
	if _, err := parseRotateFlags(srwRotation, []string{"-service-stopped", "everything"}); err == nil {
		t.Fatal("a stray argument was accepted")
	}
}

// The machine's half of the assertion: a live lease means a run is in flight,
// whatever the operator believes.
func TestRequireIdleRefusesWhileALeaseIsHeld(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()

	locker, err := jobs.NewLeaseLocker(backing, jobs.LeaseOptions{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	release, err := locker.Acquire(ctx, "space-a")
	if err != nil {
		t.Fatal(err)
	}

	if err := requireIdle(ctx, backing); err == nil {
		t.Fatal("rotation was allowed while a run held a lease")
	} else if !strings.Contains(err.Error(), "lease") {
		t.Fatalf("err = %v, want it to explain the wait", err)
	}

	release()
	if err := requireIdle(ctx, backing); err != nil {
		t.Fatalf("requireIdle after the run finished: %v", err)
	}
}
