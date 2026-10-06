package cs3

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// pathRecorder serves a fixed body and records the paths it was asked for.
type pathRecorder struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
}

func newPathRecorder(t *testing.T) *pathRecorder {
	t.Helper()
	r := &pathRecorder{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.paths = append(r.paths, req.URL.EscapedPath())
		r.mu.Unlock()
		if req.Method == http.MethodPut {
			_, _ = io.Copy(io.Discard, req.Body)
			w.WriteHeader(http.StatusCreated)
			return
		}
		_, _ = io.WriteString(w, "bytes")
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *pathRecorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

func mustOrigin(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := ParseDataServerOrigin(raw)
	if err != nil {
		t.Fatalf("ParseDataServerOrigin(%q): %v", raw, err)
	}
	return u
}

// OpenCloud 7.5 hands out its data server's own address, which from this
// service's host is "localhost" on the wrong machine. The override sends the
// request to a reachable origin and keeps the path OpenCloud chose.
func TestOpenFile_UsesTheDataServerOrigin(t *testing.T) {
	srv := newPathRecorder(t)
	fg := &fakeGateway{downloadEndpoint: "http://localhost:9158/data/spaces/st$sp%21sp/docs/a.txt"}
	c := newClient(fg, WithHTTPClient(srv.Client()), WithDataServerOrigin(mustOrigin(t, srv.URL)))

	rc, err := c.OpenFile(context.Background(), testSpace(), "docs/a.txt", 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if got := readAllAndClose(t, rc); got != "bytes" {
		t.Fatalf("body = %q", got)
	}
	if got := srv.seen(); len(got) != 1 || got[0] != "/data/spaces/st$sp%21sp/docs/a.txt" {
		t.Fatalf("paths = %v, want the gateway's path unchanged", got)
	}
}

func TestUpload_UsesTheDataServerOrigin(t *testing.T) {
	srv := newPathRecorder(t)
	fg := &fakeGateway{uploadEndpoint: "http://localhost:9158/data/simple/upload-id", uploadProtocol: "simple"}
	c := newClient(fg, WithHTTPClient(srv.Client()), WithDataServerOrigin(mustOrigin(t, srv.URL)))

	if err := c.Upload(context.Background(), testSpace(), "f.txt", 3, time.Time{}, strings.NewReader("abc")); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if got := srv.seen(); len(got) != 1 || got[0] != "/data/simple/upload-id" {
		t.Fatalf("paths = %v, want the gateway's path unchanged", got)
	}
}

// Up to OpenCloud 7.4 the gateway hands out the public data gateway with a
// transfer token. That URL is reachable as it is, and the data server would
// refuse it ("invalid upload path", 7.3.0), so the override must leave it
// alone — the same configuration has to work on both sides of 7.5.
func TestOpenFile_LeavesTheDataGatewayAlone(t *testing.T) {
	gw := newPathRecorder(t)
	fg := &fakeGateway{downloadEndpoint: gw.URL + "/data", downloadToken: "transfer"}
	c := newClient(fg, WithHTTPClient(gw.Client()),
		WithDataServerOrigin(&url.URL{Scheme: "http", Host: "127.0.0.1:1"}))

	rc, err := c.OpenFile(context.Background(), testSpace(), "docs/a.txt", 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	_ = readAllAndClose(t, rc)
	if got := gw.seen(); len(got) != 1 || got[0] != "/data" {
		t.Fatalf("paths = %v, want the data gateway used as returned", got)
	}
}

func TestUpload_LeavesTheDataGatewayAlone(t *testing.T) {
	gw := newPathRecorder(t)
	fg := &fakeGateway{uploadEndpoint: gw.URL + "/data", uploadToken: "transfer", uploadProtocol: "simple"}
	c := newClient(fg, WithHTTPClient(gw.Client()),
		WithDataServerOrigin(&url.URL{Scheme: "http", Host: "127.0.0.1:1"}))

	if err := c.Upload(context.Background(), testSpace(), "f.txt", 3, time.Time{}, strings.NewReader("abc")); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if got := gw.seen(); len(got) != 1 || got[0] != "/data" {
		t.Fatalf("paths = %v, want the data gateway used as returned", got)
	}
}

func TestDataEndpoint_UntouchedWithoutOverride(t *testing.T) {
	c := &Client{}
	const in = "https://cloud.example/data/abc"
	if got, err := c.dataEndpoint(in, ""); err != nil || got != in {
		t.Fatalf("dataEndpoint = %q, %v", got, err)
	}
}

func TestDataEndpoint_RejectsAnUnparseableEndpointWithoutEchoingIt(t *testing.T) {
	c := &Client{dataOrigin: &url.URL{Scheme: "http", Host: "opencloud:9158"}}
	_, err := c.dataEndpoint("http://internal-host:9158/%zz", "")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "internal-host") {
		t.Fatalf("error echoes the endpoint: %v", err)
	}
}

func TestParseDataServerOrigin(t *testing.T) {
	good := map[string]string{
		"http://opencloud.files.svc.cluster.local:9158":  "http://opencloud.files.svc.cluster.local:9158",
		"http://opencloud.files.svc.cluster.local:9158/": "http://opencloud.files.svc.cluster.local:9158",
		"https://10.0.0.1": "https://10.0.0.1",
	}
	for in, want := range good {
		u, err := ParseDataServerOrigin(in)
		if err != nil || u.String() != want {
			t.Errorf("ParseDataServerOrigin(%q) = %v, %v; want %s", in, u, err, want)
		}
	}
	for _, in := range []string{
		"",
		"opencloud:9158",
		"ftp://opencloud:9158",
		"http://",
		"http://opencloud:9158/data",
		"http://opencloud:9158?x=1",
		"http://user:pw@opencloud:9158",
		"http://opencloud:9158/#frag",
	} {
		if _, err := ParseDataServerOrigin(in); err == nil {
			t.Errorf("ParseDataServerOrigin(%q) accepted", in)
		}
	}
}
