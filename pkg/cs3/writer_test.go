package cs3

// The write path (restore Path B). What matters here is what a real reva
// deployment is strict about: space-relative references, the transfer token, an
// announced content length, and not silently swallowing failures.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
)

// uploadProtocols builds protocol entries as {name, endpoint, token} triples.
func uploadProtocols(specs ...[3]string) []*gateway.FileUploadProtocol {
	out := make([]*gateway.FileUploadProtocol, 0, len(specs))
	for _, s := range specs {
		out = append(out, &gateway.FileUploadProtocol{
			Protocol: s[0], UploadEndpoint: s[1], Token: s[2],
		})
	}
	return out
}

var restoreMTime = time.Date(2021, 3, 14, 15, 9, 26, 0, time.UTC)

// capturedUpload records what the data gateway received.
type capturedUpload struct {
	method        string
	body          []byte
	accessToken   string
	transferToken string
	contentLength int64
	mtime         string
}

// uploadServer stands in for reva's data gateway.
func uploadServer(t *testing.T, status int) (*httptest.Server, *capturedUpload) {
	t.Helper()
	var got capturedUpload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = capturedUpload{
			method:        r.Method,
			body:          body,
			accessToken:   r.Header.Get(TokenHeader),
			transferToken: r.Header.Get(TransferHeader),
			contentLength: r.ContentLength,
			mtime:         r.Header.Get(mtimeHeader),
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func writerFixture(t *testing.T, status int) (*Client, *fakeGateway, *capturedUpload) {
	t.Helper()
	srv, got := uploadServer(t, status)
	fg := &fakeGateway{
		authToken:      "access-token",
		uploadEndpoint: srv.URL + "/data/upload",
		uploadToken:    "transfer-token",
	}
	client := NewClient(fg, ServiceAccountAuth{Gateway: fg, ClientID: "svc", Secret: "s"},
		WithHTTPClient(srv.Client()))
	return client, fg, got
}

func TestUploadStreamsToTheDataGateway(t *testing.T) {
	client, fg, got := writerFixture(t, http.StatusOK)
	body := []byte("restored content")

	err := client.Upload(context.Background(), testSpace(), "Restore/2026/readme.txt",
		int64(len(body)), restoreMTime, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	if got.method != http.MethodPut {
		t.Fatalf("method = %s, want PUT", got.method)
	}
	if !bytes.Equal(got.body, body) {
		t.Fatalf("body = %q, want %q", got.body, body)
	}
	if got.accessToken != "access-token" || got.transferToken != "transfer-token" {
		t.Fatalf("tokens = %q / %q", got.accessToken, got.transferToken)
	}
	if got.contentLength != int64(len(body)) {
		t.Fatalf("content length = %d, want %d", got.contentLength, len(body))
	}
	// mtime is in backup scope, so it is requested on the way back in.
	if got.mtime == "" {
		t.Error("upload did not ask the server to preserve the modification time")
	}

	// The reference must be space-relative: a bare resource id resolves to "/"
	// on the data server and fails (phase-0-findings Spike 3).
	if fg.uploadedRefPath != "./Restore/2026/readme.txt" {
		t.Fatalf("upload reference path = %q", fg.uploadedRefPath)
	}
	if fg.uploadOpaque[uploadLengthHeader] != "16" {
		t.Fatalf("opaque upload length = %q, want 16", fg.uploadOpaque[uploadLengthHeader])
	}
	if fg.uploadOpaque[mtimeHeader] == "" {
		t.Error("opaque did not carry the modification time")
	}
}

func TestUploadRejectsInvalidArguments(t *testing.T) {
	client, _, _ := writerFixture(t, http.StatusOK)
	ctx := context.Background()

	if err := client.Upload(ctx, testSpace(), "", 0, restoreMTime, strings.NewReader("")); err == nil {
		t.Error("an empty path must be rejected")
	}
	if err := client.Upload(ctx, testSpace(), "f", -1, restoreMTime, strings.NewReader("")); err == nil {
		t.Error("a negative size must be rejected")
	}
}

func TestUploadSurfacesGatewayAndTransportFailures(t *testing.T) {
	ctx := context.Background()

	client, fg, _ := writerFixture(t, http.StatusOK)
	fg.uploadStatus = rpc.Code_CODE_PERMISSION_DENIED
	err := client.Upload(ctx, testSpace(), "f", 0, restoreMTime, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "PERMISSION_DENIED") {
		t.Fatalf("err = %v, want the gateway status", err)
	}

	// An existing target must be reported distinctly: a restore never overwrites.
	client, fg, _ = writerFixture(t, http.StatusOK)
	fg.uploadStatus = rpc.Code_CODE_ALREADY_EXISTS
	if err := client.Upload(ctx, testSpace(), "f", 0, restoreMTime, strings.NewReader("")); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("err = %v, want ErrAlreadyExists", err)
	}

	// A data-gateway error must not be reported as success, and must not echo
	// the response body.
	client, _, _ = writerFixture(t, http.StatusInternalServerError)
	err = client.Upload(ctx, testSpace(), "f", 4, restoreMTime, strings.NewReader("data"))
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v, want an upload failure carrying the status", err)
	}
}

func TestUploadWithoutAnEndpointFails(t *testing.T) {
	fg := &fakeGateway{authToken: "t"}
	client := NewClient(fg, ServiceAccountAuth{Gateway: fg, ClientID: "svc", Secret: "s"})

	err := client.Upload(context.Background(), testSpace(), "f", 0, restoreMTime, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "no upload endpoint") {
		t.Fatalf("err = %v, want a missing-endpoint error", err)
	}
}

func TestMakeDirIsIdempotentAndSpaceRelative(t *testing.T) {
	client, fg, _ := writerFixture(t, http.StatusOK)
	ctx := context.Background()

	if err := client.MakeDir(ctx, testSpace(), "Restore/2026-01-01"); err != nil {
		t.Fatalf("MakeDir: %v", err)
	}
	if len(fg.createdDirs) != 1 || fg.createdDirs[0] != "./Restore/2026-01-01" {
		t.Fatalf("created dirs = %v", fg.createdDirs)
	}

	// An existing directory is not an error: a restore walks the tree top-down
	// and may re-announce parents.
	fg.createStatus = rpc.Code_CODE_ALREADY_EXISTS
	if err := client.MakeDir(ctx, testSpace(), "Restore/2026-01-01"); err != nil {
		t.Fatalf("MakeDir (existing): %v", err)
	}

	// The space root always exists and needs no call.
	before := len(fg.createdDirs)
	if err := client.MakeDir(ctx, testSpace(), ""); err != nil {
		t.Fatalf("MakeDir(root): %v", err)
	}
	if len(fg.createdDirs) != before {
		t.Fatal("creating the space root must not call the gateway")
	}

	fg.createStatus = rpc.Code_CODE_PERMISSION_DENIED
	if err := client.MakeDir(ctx, testSpace(), "Restore/nope"); err == nil {
		t.Fatal("a denied creation must fail")
	}
}

func TestPickUploadProtocolPrefersSimple(t *testing.T) {
	endpoint, token := pickUploadProtocol(uploadProtocols(
		[3]string{"tus", "http://tus", "tus-token"},
		[3]string{"simple", "http://simple", "simple-token"},
	))
	if endpoint != "http://simple" || token != "simple-token" {
		t.Fatalf("picked %q/%q, want the simple protocol", endpoint, token)
	}

	// Any endpoint is better than none when "simple" is not offered.
	endpoint, token = pickUploadProtocol(uploadProtocols([3]string{"tus", "http://tus", "tus-token"}))
	if endpoint != "http://tus" || token != "tus-token" {
		t.Fatalf("picked %q/%q, want the fallback protocol", endpoint, token)
	}

	if endpoint, _ := pickUploadProtocol(nil); endpoint != "" {
		t.Fatalf("picked %q from no protocols", endpoint)
	}
}
