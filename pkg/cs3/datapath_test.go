package cs3

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
)

// testSpace is the space used by the data-path tests; its root triple is
// deliberately different from the composite space id so tests can prove the
// root is used verbatim rather than reconstructed.
func testSpace() Space {
	return Space{
		ID:   "storage-1$space-1",
		Name: "Alice",
		Type: "personal",
		Root: ResourceID{StorageID: "storage-1", SpaceID: "space-1", OpaqueID: "root-1"},
	}
}

func newClient(fg *fakeGateway, opts ...ClientOption) *Client {
	return NewClient(fg, StaticTokenAuth{Value: "reva-token"}, opts...)
}

func TestListDir_BuildsSpaceRelativeReference(t *testing.T) {
	fg := &fakeGateway{
		dirs: map[string][]*provider.ResourceInfo{
			"./docs": {fileInfo("notes.txt", 12, 1615734566)},
		},
	}
	c := newClient(fg)

	entries, err := c.ListDir(context.Background(), testSpace(), "docs")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	want := []Entry{{Path: "docs/notes.txt", Size: 12, MTimeUnix: 1615734566}}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %+v, want %+v", entries, want)
	}
	if got := fg.listedPaths; len(got) != 1 || got[0] != "./docs" {
		t.Fatalf("reference path = %v, want [./docs]", got)
	}
}

func TestListDir_RootUsesDotReference(t *testing.T) {
	fg := &fakeGateway{dirs: map[string][]*provider.ResourceInfo{}}
	c := newClient(fg)

	// Every spelling of "the root" must produce reva's relative-root path ".".
	for _, in := range []string{"", ".", "./", "/"} {
		fg.listedPaths = nil
		if _, err := c.ListDir(context.Background(), testSpace(), in); err != nil {
			t.Fatalf("ListDir(%q): %v", in, err)
		}
		if got := fg.listedPaths; len(got) != 1 || got[0] != "." {
			t.Fatalf("ListDir(%q) reference path = %v, want [.]", in, got)
		}
	}
}

func TestListDir_NonOKStatus(t *testing.T) {
	fg := &fakeGateway{listStatus: rpc.Code_CODE_PERMISSION_DENIED}
	c := newClient(fg)
	if _, err := c.ListDir(context.Background(), testSpace(), ""); err == nil {
		t.Fatal("expected non-OK ListContainer status to error")
	}
}

func TestWalk_DepthFirstWithRelativePaths(t *testing.T) {
	fg := &fakeGateway{
		dirs: map[string][]*provider.ResourceInfo{
			".":             {dirInfo("docs"), fileInfo("readme.txt", 3, 10)},
			"./docs":        {dirInfo("nested"), fileInfo("notes.txt", 5, 11)},
			"./docs/nested": {fileInfo("deep.bin", 7, 12)},
		},
	}
	c := newClient(fg)

	var got []Entry
	if err := c.Walk(context.Background(), testSpace(), func(e Entry) error {
		got = append(got, e)
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}

	want := []Entry{
		{Path: "docs", IsDir: true, MTimeUnix: 100},
		{Path: "docs/nested", IsDir: true, MTimeUnix: 100},
		{Path: "docs/nested/deep.bin", Size: 7, MTimeUnix: 12},
		{Path: "docs/notes.txt", Size: 5, MTimeUnix: 11},
		{Path: "readme.txt", Size: 3, MTimeUnix: 10},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("walk order/paths = %+v, want %+v", got, want)
	}
}

func TestWalk_PropagatesCallbackError(t *testing.T) {
	fg := &fakeGateway{
		dirs: map[string][]*provider.ResourceInfo{".": {fileInfo("a.txt", 1, 1)}},
	}
	c := newClient(fg)

	sentinel := io.ErrUnexpectedEOF
	err := c.Walk(context.Background(), testSpace(), func(Entry) error { return sentinel })
	if err != sentinel {
		t.Fatalf("Walk error = %v, want %v", err, sentinel)
	}
}

// downloadServer serves body and records the headers it saw.
type downloadServer struct {
	*httptest.Server
	gotAccessToken   string
	gotTransferToken string
	gotRange         string
	honourRange      bool
}

func newDownloadServer(t *testing.T, body []byte, honourRange bool) *downloadServer {
	t.Helper()
	ds := &downloadServer{honourRange: honourRange}
	ds.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ds.gotAccessToken = r.Header.Get(TokenHeader)
		ds.gotTransferToken = r.Header.Get(TransferHeader)
		ds.gotRange = r.Header.Get("Range")

		if ds.honourRange && ds.gotRange != "" {
			start := parseOpenEndedRange(t, ds.gotRange)
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(body[start:])
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(ds.Close)
	return ds
}

// parseOpenEndedRange parses the only Range form the client emits: "bytes=N-".
func parseOpenEndedRange(t *testing.T, h string) int64 {
	t.Helper()
	spec := strings.TrimSuffix(strings.TrimPrefix(h, "bytes="), "-")
	n, err := strconv.ParseInt(spec, 10, 64)
	if err != nil {
		t.Fatalf("unexpected Range header %q: %v", h, err)
	}
	return n
}

func TestOpenFile_StreamsWithBothTokens(t *testing.T) {
	body := []byte("hello backup world")
	ds := newDownloadServer(t, body, false)
	fg := &fakeGateway{downloadEndpoint: ds.URL, downloadToken: "transfer-xyz"}
	c := newClient(fg, WithHTTPClient(ds.Client()))

	rc, err := c.OpenFile(context.Background(), testSpace(), "docs/notes.txt", 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body = %q, want %q", got, body)
	}
	if ds.gotAccessToken != "reva-token" {
		t.Fatalf("access token header = %q", ds.gotAccessToken)
	}
	if ds.gotTransferToken != "transfer-xyz" {
		t.Fatalf("transfer token header = %q", ds.gotTransferToken)
	}
	if fg.downloadedRefPath != "./docs/notes.txt" {
		t.Fatalf("download reference path = %q, want ./docs/notes.txt", fg.downloadedRefPath)
	}
	if got := fg.downloadRootID; got.GetStorageId() != "storage-1" ||
		got.GetSpaceId() != "space-1" || got.GetOpaqueId() != "root-1" {
		t.Fatalf("download reference used wrong root: %+v", got)
	}
}

func TestOpenFile_OffsetHonouredRange(t *testing.T) {
	body := []byte("0123456789")
	ds := newDownloadServer(t, body, true)
	fg := &fakeGateway{downloadEndpoint: ds.URL}
	c := newClient(fg, WithHTTPClient(ds.Client()))

	rc, err := c.OpenFile(context.Background(), testSpace(), "f.bin", 4)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = rc.Close() }()

	got, _ := io.ReadAll(rc)
	if string(got) != "456789" {
		t.Fatalf("ranged read = %q, want 456789", got)
	}
	if ds.gotRange != "bytes=4-" {
		t.Fatalf("Range header = %q", ds.gotRange)
	}
}

func TestOpenFile_OffsetFallbackWhenRangeIgnored(t *testing.T) {
	body := []byte("0123456789")
	ds := newDownloadServer(t, body, false) // replies 200 with the whole body
	fg := &fakeGateway{downloadEndpoint: ds.URL}
	c := newClient(fg, WithHTTPClient(ds.Client()))

	rc, err := c.OpenFile(context.Background(), testSpace(), "f.bin", 4)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = rc.Close() }()

	got, _ := io.ReadAll(rc)
	if string(got) != "456789" {
		t.Fatalf("fallback read = %q, want 456789", got)
	}
}

func TestOpenFile_RejectsBadInput(t *testing.T) {
	fg := &fakeGateway{downloadEndpoint: "http://example.invalid"}
	c := newClient(fg)

	if _, err := c.OpenFile(context.Background(), testSpace(), "f.bin", -1); err == nil {
		t.Fatal("negative offset must error")
	}
	if _, err := c.OpenFile(context.Background(), testSpace(), "", 0); err == nil {
		t.Fatal("empty path must error")
	}
}

func TestOpenFile_NoEndpoint(t *testing.T) {
	fg := &fakeGateway{downloadEndpoint: ""}
	c := newClient(fg)
	if _, err := c.OpenFile(context.Background(), testSpace(), "f.bin", 0); err == nil {
		t.Fatal("missing download endpoint must error")
	}
}

func TestOpenFile_HTTPErrorStatusDoesNotLeakBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal reva detail: /var/lib/opencloud/storage/users/...", http.StatusInternalServerError)
	}))
	defer srv.Close()

	fg := &fakeGateway{downloadEndpoint: srv.URL}
	c := newClient(fg, WithHTTPClient(srv.Client()))

	_, err := c.OpenFile(context.Background(), testSpace(), "f.bin", 0)
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if got := err.Error(); bytes.Contains([]byte(got), []byte("/var/lib/opencloud")) {
		t.Fatalf("error leaked upstream body: %q", got)
	}
}

func TestOpenFile_ErrorNeverContainsTokens(t *testing.T) {
	fg := &fakeGateway{downloadEndpoint: "http://127.0.0.1:1/nope", downloadToken: "transfer-secret"}
	c := newClient(fg)

	_, err := c.OpenFile(context.Background(), testSpace(), "f.bin", 0)
	if err == nil {
		t.Fatal("expected connection error")
	}
	msg := err.Error()
	for _, secret := range []string{"reva-token", "transfer-secret"} {
		if bytes.Contains([]byte(msg), []byte(secret)) {
			t.Fatalf("error leaked %q: %s", secret, msg)
		}
	}
}

func TestPickDownloadProtocol(t *testing.T) {
	protocols := []*gateway.FileDownloadProtocol{
		{Protocol: "simple", DownloadEndpoint: "http://simple", Token: "t-simple"},
		{Protocol: "spaces", DownloadEndpoint: "http://spaces", Token: "t-spaces"},
	}
	if ep, tok := pickDownloadProtocol(protocols); ep != "http://spaces" || tok != "t-spaces" {
		t.Fatalf("spaces protocol not preferred: %q %q", ep, tok)
	}

	// Falls back to the first protocol that advertises an endpoint.
	fallback := []*gateway.FileDownloadProtocol{
		{Protocol: "simple", DownloadEndpoint: "", Token: "ignored"},
		{Protocol: "simple", DownloadEndpoint: "http://simple", Token: "t-simple"},
	}
	if ep, tok := pickDownloadProtocol(fallback); ep != "http://simple" || tok != "t-simple" {
		t.Fatalf("fallback protocol not selected: %q %q", ep, tok)
	}

	if ep, _ := pickDownloadProtocol(nil); ep != "" {
		t.Fatalf("empty protocol list must yield no endpoint, got %q", ep)
	}
}

func TestSplitSpaceID(t *testing.T) {
	cases := []struct {
		in                                 string
		wantStorage, wantSpace, wantOpaque string
	}{
		{"plain", "plain", "plain", "plain"},
		{"storage$space", "storage", "space", "space"},
		{"storage$space!node", "storage", "space", "node"},
	}
	for _, tc := range cases {
		st, sp, op := splitSpaceID(tc.in)
		if st != tc.wantStorage || sp != tc.wantSpace || op != tc.wantOpaque {
			t.Fatalf("splitSpaceID(%q) = (%q,%q,%q), want (%q,%q,%q)",
				tc.in, st, sp, op, tc.wantStorage, tc.wantSpace, tc.wantOpaque)
		}
	}
}

func TestSpaceRootID_FallsBackToCompositeSplit(t *testing.T) {
	// No Root triple: the composite id must be split rather than duplicated.
	got := spaceRootID(Space{ID: "storage-9$space-9!node-9"})
	if got.GetStorageId() != "storage-9" || got.GetSpaceId() != "space-9" || got.GetOpaqueId() != "node-9" {
		t.Fatalf("spaceRootID fallback = %+v", got)
	}
}

func TestCleanRel(t *testing.T) {
	cases := map[string]string{
		"":      "",
		".":     "",
		"./":    "",
		"/":     "",
		"./a/b": "a/b",
		"/a/b/": "a/b",
		"a//b":  "a/b",
		"a/./b": "a/b",
	}
	for in, want := range cases {
		if got := cleanRel(in); got != want {
			t.Fatalf("cleanRel(%q) = %q, want %q", in, got, want)
		}
	}
}
