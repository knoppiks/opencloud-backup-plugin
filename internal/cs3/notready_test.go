package cs3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
)

// fakeClock runs the retry wait without taking its time: every pause moves the
// clock forward by exactly what was asked for.
type fakeClock struct {
	t      time.Time
	pauses []time.Duration
}

func (f *fakeClock) now() time.Time { return f.t }

func (f *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.pauses = append(f.pauses, d)
	f.t = f.t.Add(d)
	return nil
}

func (f *fakeClock) total() time.Duration {
	var sum time.Duration
	for _, d := range f.pauses {
		sum += d
	}
	return sum
}

// withFakeClock installs a fake clock on c.
func withFakeClock(c *Client) *fakeClock {
	clk := &fakeClock{t: time.Date(2026, 9, 29, 2, 0, 0, 0, time.UTC)}
	c.now, c.sleep = clk.now, clk.sleep
	return clk
}

// tooEarlyServer answers 425 to the first `refusals` requests, then serves
// body. It records the Range header of every request.
type tooEarlyServer struct {
	*httptest.Server
	requests atomic.Int32

	mu     sync.Mutex
	ranges []string
}

func (s *tooEarlyServer) seenRanges() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ranges...)
}

func newTooEarlyServer(t *testing.T, refusals int32, body string) *tooEarlyServer {
	t.Helper()
	s := &tooEarlyServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.ranges = append(s.ranges, r.Header.Get("Range"))
		s.mu.Unlock()
		if s.requests.Add(1) <= refusals {
			http.Error(w, "file is processing", http.StatusTooEarly)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func readAllAndClose(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(got)
}

// A file uploaded moments before it is read is answered with 425 while OpenCloud
// post-processes it. That is "not yet", and the read succeeds once it is ready.
func TestOpenFile_RetriesAFileStillBeingProcessed(t *testing.T) {
	srv := newTooEarlyServer(t, 2, "fresh upload")
	fg := &fakeGateway{downloadEndpoint: srv.URL}
	c := newClient(fg, WithHTTPClient(srv.Client()))
	clk := withFakeClock(c)

	rc, err := c.OpenFile(context.Background(), testSpace(), "new.txt", 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if got := readAllAndClose(t, rc); got != "fresh upload" {
		t.Fatalf("body = %q", got)
	}
	if got := srv.requests.Load(); got != 3 {
		t.Fatalf("requests = %d, want 3", got)
	}
	// Each attempt initiates its own download: transfer tokens are not reused.
	if fg.downloadCalls != 3 {
		t.Fatalf("InitiateFileDownload calls = %d, want 3", fg.downloadCalls)
	}
	want := []time.Duration{notReadyFirstDelay, 2 * notReadyFirstDelay}
	if !reflect.DeepEqual(clk.pauses, want) {
		t.Fatalf("pauses = %v, want %v", clk.pauses, want)
	}
}

// The gateway may say "too early" itself, before any byte is requested.
func TestOpenFile_RetriesTooEarlyFromTheGateway(t *testing.T) {
	ds := newDownloadServer(t, []byte("ready now"), false)
	fg := &fakeGateway{
		downloadEndpoint:  ds.URL,
		downloadStatusSeq: []rpc.Code{rpc.Code_CODE_TOO_EARLY, rpc.Code_CODE_TOO_EARLY},
	}
	c := newClient(fg, WithHTTPClient(ds.Client()))
	withFakeClock(c)

	rc, err := c.OpenFile(context.Background(), testSpace(), "new.txt", 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if got := readAllAndClose(t, rc); got != "ready now" {
		t.Fatalf("body = %q", got)
	}
	if fg.downloadCalls != 3 {
		t.Fatalf("InitiateFileDownload calls = %d, want 3", fg.downloadCalls)
	}
}

// A resumed read keeps its position across retries.
func TestOpenFile_RetryKeepsTheOffset(t *testing.T) {
	srv := newTooEarlyServer(t, 1, "0123456789")
	fg := &fakeGateway{downloadEndpoint: srv.URL}
	c := newClient(fg, WithHTTPClient(srv.Client()))
	withFakeClock(c)

	rc, err := c.OpenFile(context.Background(), testSpace(), "f.bin", 4)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if got := readAllAndClose(t, rc); got != "456789" {
		t.Fatalf("body = %q, want 456789", got)
	}
	if want := []string{"bytes=4-", "bytes=4-"}; !reflect.DeepEqual(srv.seenRanges(), want) {
		t.Fatalf("Range headers = %v, want %v", srv.seenRanges(), want)
	}
}

// A file that never becomes readable fails the read once the window is used
// up — never sooner, never later, and never as a silent success.
func TestOpenFile_GivesUpWhenTheWindowRunsOut(t *testing.T) {
	srv := newTooEarlyServer(t, 1<<30, "")
	fg := &fakeGateway{downloadEndpoint: srv.URL}
	window := 40 * time.Second
	c := newClient(fg, WithHTTPClient(srv.Client()), WithNotReadyWait(window))
	clk := withFakeClock(c)

	rc, err := c.OpenFile(context.Background(), testSpace(), "stuck.bin", 0)
	if rc != nil {
		_ = rc.Close()
		t.Fatal("a reader was returned for a file that never became ready")
	}
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v, want ErrNotReady", err)
	}
	if got := clk.total(); got != window {
		t.Fatalf("waited %v, want exactly the window %v", got, window)
	}
	for _, d := range clk.pauses {
		if d > notReadyMaxDelay {
			t.Fatalf("pause %v exceeds the cap %v", d, notReadyMaxDelay)
		}
	}
}

// The run deadline outranks the retry window.
func TestOpenFile_StopsWaitingWhenTheContextEnds(t *testing.T) {
	srv := newTooEarlyServer(t, 1<<30, "")
	fg := &fakeGateway{downloadEndpoint: srv.URL}
	c := newClient(fg, WithHTTPClient(srv.Client()))
	withFakeClock(c)

	ctx, cancel := context.WithCancel(context.Background())
	c.sleep = func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}

	_, err := c.OpenFile(ctx, testSpace(), "stuck.bin", 0)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v, want both context.Canceled and ErrNotReady", err)
	}
	if got := srv.requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

// Only "too early" is retried. Anything else is answered at once, as before.
func TestOpenFile_DoesNotRetryOtherFailures(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError, http.StatusUnauthorized} {
		var requests atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			w.WriteHeader(status)
		}))
		fg := &fakeGateway{downloadEndpoint: srv.URL}
		c := newClient(fg, WithHTTPClient(srv.Client()))
		clk := withFakeClock(c)

		_, err := c.OpenFile(context.Background(), testSpace(), "f.bin", 0)
		srv.Close()
		if err == nil || errors.Is(err, ErrNotReady) {
			t.Fatalf("status %d: err = %v, want a non-ErrNotReady failure", status, err)
		}
		if requests.Load() != 1 || len(clk.pauses) != 0 {
			t.Fatalf("status %d: retried (%d requests, pauses %v)", status, requests.Load(), clk.pauses)
		}
	}
}

func TestOpenFile_DoesNotRetryOtherGatewayCodes(t *testing.T) {
	fg := &fakeGateway{downloadEndpoint: "http://example.invalid", downloadStatus: rpc.Code_CODE_PERMISSION_DENIED}
	c := newClient(fg)
	clk := withFakeClock(c)

	_, err := c.OpenFile(context.Background(), testSpace(), "f.bin", 0)
	if err == nil || errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v, want a non-ErrNotReady failure", err)
	}
	if fg.downloadCalls != 1 || len(clk.pauses) != 0 {
		t.Fatalf("retried (%d calls, pauses %v)", fg.downloadCalls, clk.pauses)
	}
}

func TestNotReadyWindow(t *testing.T) {
	if got := (&Client{}).notReadyWindow(); got != DefaultNotReadyWait {
		t.Fatalf("default window = %v, want %v", got, DefaultNotReadyWait)
	}
	if got := NewClient(&fakeGateway{}, StaticTokenAuth{}, WithNotReadyWait(0)).notReadyWindow(); got != DefaultNotReadyWait {
		t.Fatalf("zero window = %v, want the default", got)
	}
	if got := NewClient(&fakeGateway{}, StaticTokenAuth{}, WithNotReadyWait(7*time.Second)).notReadyWindow(); got != 7*time.Second {
		t.Fatalf("configured window = %v, want 7s", got)
	}
}

func TestSleepCtx(t *testing.T) {
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("sleepCtx: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepCtx on a cancelled context = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("sleepCtx did not return when its context ended")
	}
}

func TestClientClockDefaultsToRealTime(t *testing.T) {
	c := &Client{}
	if d := time.Since(c.clock()); d < 0 || d > time.Minute {
		t.Fatalf("clock is off by %v", d)
	}
	if err := c.pause(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("pause: %v", err)
	}
}
