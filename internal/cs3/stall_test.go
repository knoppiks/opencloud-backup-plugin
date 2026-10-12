package cs3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
)

// A gateway that sends a byte and then falls silent must not hold a run open.
// This is what a scheduled backup runs into when the storage backend hangs, and
// without the guard it is unbounded: the transfer is "in progress" forever.
func TestOpenFile_FailsATransferThatStopsMakingProgress(t *testing.T) {
	stalled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("first chunk"))
		w.(http.Flusher).Flush()
		// Never write again, and never end the response.
		select {
		case <-stalled:
		case <-r.Context().Done():
		}
	}))
	// Order matters: the handler is released before the server is closed, or
	// Close waits forever for a request nobody ended.
	defer srv.Close()
	defer close(stalled)

	c, space := stallClient(t, srv.URL, 100*time.Millisecond)

	body, err := c.OpenFile(context.Background(), space, "big.bin", 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = body.Close() }()

	start := time.Now()
	_, err = io.ReadAll(body)
	if !errors.Is(err, ErrTransferStalled) {
		t.Fatalf("read = %v, want ErrTransferStalled", err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("the read was not bounded: %v", elapsed)
	}
}

// The guard measures progress, not elapsed time: a transfer that keeps trickling
// must be allowed to take longer than the stall timeout.
func TestOpenFile_AllowsASlowButProgressingTransfer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 10; i++ {
			_, _ = w.Write([]byte("chunk"))
			w.(http.Flusher).Flush()
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer srv.Close()

	c, space := stallClient(t, srv.URL, 100*time.Millisecond)

	body, err := c.OpenFile(context.Background(), space, "slow.bin", 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = body.Close() }()

	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The whole transfer took ~200ms, twice the stall timeout, and must survive.
	if len(got) != 50 {
		t.Fatalf("read %d bytes, want 50", len(got))
	}
}

// A gateway that stops accepting bytes mid-upload stalls the restore just as a
// silent download stalls a backup, and is failed the same way.
func TestUpload_FailsWhenTheGatewayStopsAcceptingBytes(t *testing.T) {
	stalled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// Never read the body, never answer.
		select {
		case <-stalled:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(stalled)

	fg := &fakeGateway{authToken: "t", uploadEndpoint: srv.URL, uploadProtocol: "simple"}
	c := NewClient(fg, ServiceAccountAuth{Gateway: fg},
		cs3TestHTTPClient(), WithStallTimeout(100*time.Millisecond))
	space := Space{ID: "s1", Root: ResourceID{StorageID: "st", SpaceID: "sp", OpaqueID: "root"}}

	// Larger than any socket buffer, so the transport really does block on the
	// write rather than handing the whole body to the kernel and waiting.
	const size = 32 << 20
	start := time.Now()
	err := c.Upload(context.Background(), space, "stuck.bin", size, time.Time{},
		io.LimitReader(zeroes{}, size))
	if !errors.Is(err, ErrTransferStalled) {
		t.Fatalf("Upload = %v, want ErrTransferStalled", err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("the upload was not bounded: %v", elapsed)
	}
}

// zeroes is an endless source of bytes.
type zeroes struct{}

func (zeroes) Read(p []byte) (int, error) { return len(p), nil }

// A finished transfer is not a stalled one: whatever the server does after the
// last byte — finalising a large upload, say — must not be cut short here.
func TestStallGuard_StopsWatchingAtEndOfInput(t *testing.T) {
	cancelled := make(chan struct{})
	g := newStallGuard(strings.NewReader("body"), func() { close(cancelled) }, 20*time.Millisecond)

	if _, err := io.ReadAll(g); err != nil {
		t.Fatalf("read: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	select {
	case <-cancelled:
		t.Fatal("the guard cancelled a request whose body was fully read")
	default:
	}
	g.stop()
}

// A guard with no timeout configured is a pass-through: nothing else in the
// package may depend on the guard being active to behave correctly.
func TestStallGuard_ZeroTimeoutIsInert(t *testing.T) {
	g := newStallGuard(strings.NewReader("body"), func() {}, 0)
	got, err := io.ReadAll(g)
	if err != nil || string(got) != "body" {
		t.Fatalf("read = %q (%v)", got, err)
	}
	g.stop()
	g.stop() // idempotent
	if g.stalled() {
		t.Fatal("an inert guard cannot have fired")
	}
}

func TestClient_StallTimeoutDefaults(t *testing.T) {
	if got := (&Client{}).stallTimeout(); got != DefaultStallTimeout {
		t.Fatalf("stallTimeout = %v, want the default", got)
	}
	if got := (&Client{stall: time.Second}).stallTimeout(); got != time.Second {
		t.Fatalf("stallTimeout = %v, want the configured value", got)
	}
}

// stallClient builds a Client whose downloads go to endpoint and whose
// transfers must make progress within timeout.
func stallClient(t *testing.T, endpoint string, timeout time.Duration) (*Client, Space) {
	t.Helper()
	fg := &fakeGateway{
		authToken:        "t",
		downloadEndpoint: endpoint,
		downloadProtocol: "spaces",
		downloadStatus:   rpc.Code_CODE_OK,
	}
	c := NewClient(fg, ServiceAccountAuth{Gateway: fg}, cs3TestHTTPClient(), WithStallTimeout(timeout))
	return c, Space{ID: "s1", Root: ResourceID{StorageID: "st", SpaceID: "sp", OpaqueID: "root"}}
}

// cs3TestHTTPClient mirrors the production transport settings that matter here:
// no overall timeout, so only the stall guard can end a transfer.
func cs3TestHTTPClient() ClientOption {
	return WithHTTPClient(&http.Client{})
}
