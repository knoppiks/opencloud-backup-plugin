package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/knoppiks/opencloud-backup-plugin/internal/cs3"
	"github.com/knoppiks/opencloud-backup-plugin/internal/cs3state"
	"github.com/knoppiks/opencloud-backup-plugin/internal/retry"
	"github.com/knoppiks/opencloud-backup-plugin/internal/state"
)

// What the gateway answered on the fixture while OpenCloud booted, and with a
// wrong secret (phase-0-findings.md, "Phase 12 spike" §4).
var (
	errBooting     = fmt.Errorf("cs3state: resolve state space: cs3 authenticate: %w", status.Error(codes.Unavailable, "connection refused"))
	errWrongSecret = fmt.Errorf("cs3state: resolve state space: %w", &cs3.StatusError{Op: "Authenticate", Code: rpc.Code_CODE_INTERNAL})
)

// checks returns errs in turn, then succeeds, counting the calls.
func checks(calls *int, errs ...error) func(context.Context) error {
	return func(context.Context) error {
		*calls++
		if len(errs) == 0 {
			return nil
		}
		err := errs[0]
		errs = errs[1:]
		return err
	}
}

// quick waits without pausing.
func quick() retry.Options {
	return retry.Options{Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }}
}

func TestWaitForStateSpace_WaitsWhileOpenCloudBoots(t *testing.T) {
	var calls int
	if err := waitForStateSpace(context.Background(), checks(&calls, errBooting, errBooting), time.Minute, quick()); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if calls != 3 {
		t.Fatalf("%d checks, want 3", calls)
	}
}

// OpenCloud answering "no" is not waited on: a wrong secret and an unsafe
// Space stay fatal at once.
func TestWaitForStateSpace_AnAnswerEndsTheWait(t *testing.T) {
	unsafe := fmt.Errorf("%w: \"Family\" has 2 member grant(s)", cs3state.ErrUnsafeStateSpace)
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"wrong secret", errWrongSecret, "OC_SERVICE_ACCOUNT_SECRET"},
		{"unsafe state space", unsafe, "member grant"},
		{"missing state space", errors.New("cs3state: the configured state space does not exist"), "does not exist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			err := waitForStateSpace(context.Background(), checks(&calls, errBooting, tc.err), time.Minute, quick())
			if err == nil || !strings.Contains(err.Error(), tc.want) || !errors.Is(err, tc.err) {
				t.Fatalf("wait = %v, want %q wrapping the answer", err, tc.want)
			}
			if calls != 2 {
				t.Fatalf("%d checks, want 2", calls)
			}
		})
	}
}

func TestWaitForStateSpace_GivesUpAfterTheTimeout(t *testing.T) {
	always := func(context.Context) error { return errBooting }
	err := waitForStateSpace(context.Background(), always, 20*time.Millisecond,
		retry.Options{Initial: time.Millisecond, Max: time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "STARTUP_WAIT_SECONDS") || !errors.Is(err, errBooting) {
		t.Fatalf("wait = %v, want the timeout naming STARTUP_WAIT_SECONDS and the last error", err)
	}
}

// Zero waits without limit: only the context ends it.
func TestWaitForStateSpace_ZeroWaitsUntilStopped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	always := func(context.Context) error { return errBooting }
	err := waitForStateSpace(ctx, always, 0, retry.Options{Initial: time.Millisecond, Max: time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "STARTUP_WAIT_SECONDS") {
		t.Fatalf("wait = %v, want the caller's context error, not a timeout of its own", err)
	}
}

func TestStateEnv_StartupWait(t *testing.T) {
	if got := testConfig(t).State.startupWait(); got != 2*time.Minute {
		t.Fatalf("default = %v, want 2m", got)
	}
	if got := testConfig(t, "STARTUP_WAIT_SECONDS=0").State.startupWait(); got != 0 {
		t.Fatalf("0 = %v, want 0 (no limit)", got)
	}
	if _, err := loadServiceEnv([]string{"STARTUP_WAIT_SECONDS=-1"}); err == nil ||
		!strings.Contains(err.Error(), "STARTUP_WAIT_SECONDS") {
		t.Fatalf("negative = %v, want refused", err)
	}
}

// freeAddr is a loopback address nothing listens on, for a serve whose port
// the test has to know.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// statusOf returns the status of GET url, or 0 while nothing answers.
func statusOf(url string) int {
	res, err := http.Get(url) //nolint:noctx // a test probe against loopback
	if err != nil {
		return 0
	}
	_ = res.Body.Close()
	return res.StatusCode
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startServe runs serve with a state store that is held back until the test
// releases it, as OpenCloud is while it boots.
func startServe(t *testing.T, base string) (url string, release chan<- struct{}, done <-chan error) {
	t.Helper()
	addr := freeAddr(t)
	cfg := testConfig(t, append(minimalServiceEnv(t), "BACKUPD_ADDR="+addr, "BACKUPD_BASE_PATH="+base)...)
	gate := make(chan struct{})
	deps := startupDeps{openState: func(ctx context.Context, _ stateEnv, _ *cs3.Client, _ string, _ *slog.Logger) (state.Store, error) {
		select {
		case <-gate:
			return state.NewMemoryStore(), nil
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for OpenCloud: %w", ctx.Err())
		}
	}}
	finished := make(chan error, 1)
	go func() { finished <- serve(cfg, discardLogger(), deps) }()

	url = "http://" + addr
	eventually(t, "the listener", func() bool { return statusOf(url+"/healthz") == http.StatusOK })
	return url, gate, finished
}

func stopServe(t *testing.T, done <-chan error) {
	t.Helper()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve = %v, want a clean stop", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not stop")
	}
}

// While startup waits for OpenCloud the service is alive and not ready; the
// API takes over once startup completes (#152).
func TestServe_ListensBeforeOpenCloudAnswers(t *testing.T) {
	// startServe saw /healthz answer 200 while the state store is held back.
	url, release, done := startServe(t, "/backup")
	if got := statusOf(url + "/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("/readyz while starting = %d, want 503", got)
	}
	if got := statusOf(url + "/backup/api/v1/spaces"); got != http.StatusServiceUnavailable {
		t.Fatalf("API while starting = %d, want 503", got)
	}

	close(release)
	eventually(t, "readiness", func() bool { return statusOf(url+"/readyz") == http.StatusOK })
	if got := statusOf(url + "/backup/api/v1/spaces"); got == http.StatusServiceUnavailable || got == 0 {
		t.Fatalf("API after startup = %d, want the API's own answer", got)
	}
	stopServe(t, done)
}

// afterReturn fails the test for every log record written once closed is
// set: something of serve that outlived it. The listener's own line is held
// up, so a listener serve does not wait for is caught every time.
type afterReturn struct {
	t      *testing.T
	closed *atomic.Bool
}

func (h afterReturn) Enabled(context.Context, slog.Level) bool { return true }
func (h afterReturn) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h afterReturn) WithGroup(string) slog.Handler            { return h }
func (h afterReturn) Handle(_ context.Context, r slog.Record) error {
	if strings.HasPrefix(r.Message, "backupd listening") {
		time.Sleep(20 * time.Millisecond)
	}
	if h.closed.Load() {
		h.t.Errorf("logged after serve returned: %q", r.Message)
	}
	return nil
}

// A startup that fails closes the listener and waits for it, so nothing of
// it logs after serve returned (the race that failed CI after #152).
func TestServe_AFailedStartupLeavesNothingRunning(t *testing.T) {
	failing := startupDeps{openState: func(context.Context, stateEnv, *cs3.Client, string, *slog.Logger) (state.Store, error) {
		return nil, errors.New("no state for this test")
	}}
	for range 5 {
		var closed atomic.Bool
		cfg := testConfig(t, append(minimalServiceEnv(t), "BACKUPD_ADDR=127.0.0.1:0")...)
		if err := serve(cfg, slog.New(afterReturn{t: t, closed: &closed}), failing); err == nil {
			t.Fatal("serve succeeded without state")
		}
		closed.Store(true)
		// Whatever was still running has had the time to log.
		time.Sleep(40 * time.Millisecond)
	}
}

// SIGTERM while waiting for OpenCloud is a clean stop.
func TestServe_StopsCleanlyWhileWaiting(t *testing.T) {
	_, _, done := startServe(t, "")
	stopServe(t, done)
}
