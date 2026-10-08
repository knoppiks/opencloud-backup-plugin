package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/targets"
)

// logCapture collects a server's JSON log lines.
type logCapture struct{ buf bytes.Buffer }

func (c *logCapture) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(&c.buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// lines returns every log line as a map.
func (c *logCapture) lines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(c.buf.Bytes()))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", sc.Text())
		}
		out = append(out, m)
	}
	return out
}

// linesFor returns the log lines carrying the request id.
func (c *logCapture) linesFor(t *testing.T, requestID string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range c.lines(t) {
		if l["request_id"] == requestID {
			out = append(out, l)
		}
	}
	return out
}

// onlyLineFor asserts the request left exactly one log line and returns it.
func (c *logCapture) onlyLineFor(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	id := rec.Header().Get(RequestIDHeader)
	if id == "" {
		t.Fatalf("response carries no %s header", RequestIDHeader)
	}
	got := c.linesFor(t, id)
	if len(got) != 1 {
		t.Fatalf("request %s left %d log lines, want exactly 1:\n%s", id, len(got), c.buf.String())
	}
	return got[0]
}

var requestIDShape = regexp.MustCompile(`^[0-9a-f]{16}$`)

func TestEveryResponseCarriesAFreshRequestID(t *testing.T) {
	srv := NewServer()
	first := authGet(srv, "/healthz", "")
	second := authGet(srv, "/api/v1/spaces", "")

	a, b := first.Header().Get(RequestIDHeader), second.Header().Get(RequestIDHeader)
	if !requestIDShape.MatchString(a) || !requestIDShape.MatchString(b) {
		t.Fatalf("request ids %q, %q: want 16 hex characters", a, b)
	}
	if a == b {
		t.Fatalf("two requests share the id %q", a)
	}
}

// A well-formed caller id (an ingress's, or OpenCloud's web client's UUID) is
// kept, so the request can be followed across services.
func TestRequestIDIsTakenFromTheCallerWhenWellFormed(t *testing.T) {
	var logs logCapture
	srv := NewServer(WithLogger(logs.logger()))
	for _, id := range []string{
		"3f2b8c1e-9d4a-4e6b-8f1a-2c7d5e9b0a13",
		"req.42_a:b-c",
		strings.Repeat("a", maxRequestIDLen),
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/spaces", nil)
		req.Header.Set(RequestIDHeader, id)
		srv.Handler().ServeHTTP(rec, req)
		if got := rec.Header().Get(RequestIDHeader); got != id {
			t.Errorf("caller id %q answered as %q", id, got)
		}
		if line := logs.onlyLineFor(t, rec); line["request_id"] != id {
			t.Errorf("logged request_id %v, want %q", line["request_id"], id)
		}
	}
}

// An id that is empty, too long or carries anything outside the token
// charset is replaced by a minted one rather than logged.
func TestMalformedRequestIDIsReplaced(t *testing.T) {
	srv := NewServer()
	for _, id := range []string{
		"",
		strings.Repeat("a", maxRequestIDLen+1),
		"has space",
		`quote"d`,
		"new\nline",
		"ünïcode",
		"a/b",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set(RequestIDHeader, id)
		srv.Handler().ServeHTTP(rec, req)
		if got := rec.Header().Get(RequestIDHeader); !requestIDShape.MatchString(got) {
			t.Errorf("caller id %q answered as %q, want a minted id", id, got)
		}
	}
}

// F3: the cause of a 5xx reaches the operator's log, on the request's one line,
// with the id the client was given; the client still sees the safe message.
func TestServerErrorLogsItsCauseOnceWithTheRequestID(t *testing.T) {
	var logs logCapture
	srv := NewServer(
		WithLogger(logs.logger()),
		WithTokenValidator(fakeValidator{tokens: map[string]string{"tok": "u"}}),
		WithSpaceReader(fakeSpaceReader{err: errors.New("cs3 gateway: internal detail")}),
	)

	rec := authGet(srv, "/api/v1/spaces", "tok")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "internal detail") {
		t.Fatalf("the cause reached the client: %s", rec.Body)
	}

	line := logs.onlyLineFor(t, rec)
	if line["level"] != "ERROR" {
		t.Fatalf("level = %v, want ERROR", line["level"])
	}
	if line["code"] != "upstream_error" || !strings.Contains(asString(line["err"]), "internal detail") {
		t.Fatalf("the log line does not name the cause: %v", line)
	}
	if line["status"] != float64(http.StatusBadGateway) || line["path"] != "/api/v1/spaces" {
		t.Fatalf("the log line does not describe the request: %v", line)
	}
}

// 503 means "not now". It is logged with what is missing, at WARN.
func TestServiceUnavailableIsLoggedAtWarnWithWhatIsMissing(t *testing.T) {
	var logs logCapture
	srv := NewServer(
		WithLogger(logs.logger()),
		WithTokenValidator(fakeValidator{tokens: map[string]string{"alice-tok": "alice"}}),
		WithSpaceReader(fakeSpaceReader{spaces: []cs3.Space{
			{ID: "space-alice", Name: "Alice", Type: "personal", Owner: "alice"},
		}}),
	)

	rec := authGet(srv, "/api/v1/spaces/space-alice/backup/keystatus", "alice-tok")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	line := logs.onlyLineFor(t, rec)
	if line["level"] != "WARN" || asString(line["err"]) != "key service not configured" {
		t.Fatalf("log line = %v, want WARN naming the missing key service", line)
	}
}

func TestSuccessfulRequestsAreLoggedAtInfoWithoutTheQuery(t *testing.T) {
	var logs logCapture
	srv := NewServer(
		WithLogger(logs.logger()),
		WithTokenValidator(fakeValidator{tokens: map[string]string{"tok": "u"}}),
		WithSpaceReader(fakeSpaceReader{}),
	)

	rec := authGet(srv, "/api/v1/spaces?filter=hidden-query-value", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	line := logs.onlyLineFor(t, rec)
	if line["level"] != "INFO" || line["path"] != "/api/v1/spaces" || line["method"] != http.MethodGet {
		t.Fatalf("log line = %v", line)
	}
	if _, ok := line["err"]; ok {
		t.Fatalf("a successful request logged an error: %v", line)
	}
	if strings.Contains(logs.buf.String(), "hidden-query-value") {
		t.Fatalf("the query string was logged:\n%s", logs.buf.String())
	}
}

// The kubelet calls the probes every few seconds; a passing probe must not
// drown the log, and a failing one must say why.
func TestProbesAreQuietWhilePassing(t *testing.T) {
	var logs logCapture
	srv := NewServer(WithLogger(logs.logger()))
	if line := logs.onlyLineFor(t, authGet(srv, "/healthz", "")); line["level"] != "DEBUG" {
		t.Fatalf("passing probe logged at %v, want DEBUG", line["level"])
	}

	failing := NewServer(WithLogger(logs.logger()),
		WithReadiness(func(context.Context) error { return errors.New("gateway not up yet") }))
	rec := authGet(failing, "/readyz", "")
	line := logs.onlyLineFor(t, rec)
	if line["level"] != "WARN" || asString(line["err"]) != "gateway not up yet" {
		t.Fatalf("failing probe line = %v, want WARN with the cause", line)
	}
	if strings.Contains(rec.Body.String(), "gateway") {
		t.Fatalf("the readiness cause reached the client: %s", rec.Body)
	}
}

// panickingSpaceReader panics on the request path.
type panickingSpaceReader struct{ fakeSpaceReader }

func (panickingSpaceReader) ListSpaces(context.Context) ([]cs3.Space, error) {
	panic("handler bug")
}

// F7: a panicking handler answers 500 and leaves one line with the panic, its
// stack and the request id; the process lives on.
func TestPanicBecomesOne500AndOneLine(t *testing.T) {
	var logs logCapture
	srv := NewServer(
		WithLogger(logs.logger()),
		WithTokenValidator(fakeValidator{tokens: map[string]string{"tok": "u"}}),
		WithSpaceReader(panickingSpaceReader{}),
	)

	rec := authGet(srv, "/api/v1/spaces", "tok")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "handler bug") {
		t.Fatalf("the panic reached the client: %s", rec.Body)
	}
	line := logs.onlyLineFor(t, rec)
	if line["level"] != "ERROR" || line["panic"] != "handler bug" || asString(line["stack"]) == "" {
		t.Fatalf("log line = %v, want the panic and its stack", line)
	}
}

// A handler that panics after answering keeps its answer: a second header
// would be a protocol error, and the status already sent is what the client saw.
func TestPanicAfterTheHeaderKeepsTheAnswer(t *testing.T) {
	var logs logCapture
	srv := NewServer(WithLogger(logs.logger()))
	h := srv.observe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		panic("late bug")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want the 202 already sent", rec.Code)
	}
	line := logs.onlyLineFor(t, rec)
	if line["level"] != "ERROR" || line["panic"] != "late bug" {
		t.Fatalf("log line = %v, want the panic at ERROR", line)
	}
}

func TestErrAbortHandlerIsReRaised(t *testing.T) {
	srv := NewServer()
	h := srv.observe(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if v := recover(); v != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler re-raised", v)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

// A base path stripped in front of the router (BACKUPD_BASE_PATH) is still in
// the log: the operator searches for the URL the browser used.
func TestLoggedPathIsTheOneTheClientAskedFor(t *testing.T) {
	var logs logCapture
	srv := NewServer(WithLogger(logs.logger()))
	h := http.StripPrefix("/backup", srv.Handler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/backup/healthz?x=1", nil))
	if line := logs.onlyLineFor(t, rec); line["path"] != "/backup/healthz" {
		t.Fatalf("path = %v, want /backup/healthz", line["path"])
	}
}

// --- noleak: the new log lines never hold what a request carries -----------

// failingSRWStore stores the recovery envelope and fails the server envelope,
// so a key setup ends in a 500 after its whole body has been read.
type failingSRWStore struct{ *keys.MemoryStore }

func (failingSRWStore) PutSRW(context.Context, string, keys.WrappedDK) error {
	return errors.New("state: gateway unavailable")
}

// The setup body carries the Data Key; a failed setup's log line must name the
// cause and nothing from the request.
func TestFailedKeySetupLogsNoKeyMaterialOrToken(t *testing.T) {
	var logs logCapture
	wrapper := newTestSRWWrapper(t)
	srv := NewServer(
		WithLogger(logs.logger()),
		WithTokenValidator(fakeValidator{tokens: map[string]string{"alice-tok": "alice"}}),
		WithSpaceReader(fakeSpaceReader{spaces: []cs3.Space{
			{ID: "space-alice", Name: "Alice", Type: "personal", Owner: "alice"},
		}}),
		WithKeyStore(failingSRWStore{keys.NewMemoryStore()}),
		WithSRWWrapper(wrapper),
	)
	body, dk, rkSecret := clientSetup(t)
	var req setupRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(srv, http.MethodPost, "/api/v1/spaces/space-alice/backup/setup?debug=query-secret", "alice-tok", body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body)
	}
	line := logs.onlyLineFor(t, rec)
	if !strings.Contains(asString(line["err"]), "gateway unavailable") {
		t.Fatalf("log line does not name the cause: %v", line)
	}

	logged := logs.buf.String()
	for name, secret := range map[string]string{
		"bearer token":             "alice-tok",
		"query string":             "query-secret",
		"data key (base64)":        req.DataKey,
		"recovery envelope":        req.WrappedDKRK,
		"data key (raw)":           string(dk),
		"recovery key":             string(rkSecret),
		"recovery key (base64)":    base64.StdEncoding.EncodeToString(rkSecret),
		"data key (base64 prefix)": req.DataKey[:12],
	} {
		if strings.Contains(logged, secret) {
			t.Fatalf("the log holds the %s:\n%s", name, logged)
		}
	}
}

// failingCreateStore refuses to create targets.
type failingCreateStore struct{ targets.Store }

func (failingCreateStore) CreateTarget(context.Context, targets.Target) (targets.Target, error) {
	return targets.Target{}, errors.New("state: gateway unavailable")
}

// The admin's create body carries target credentials; a failed create's log
// line must name the cause and no credential.
func TestFailedTargetCreateLogsNoCredentials(t *testing.T) {
	var logs logCapture
	env := newAdminTestEnv(t)
	env.srv = NewServer(
		WithLogger(logs.logger()),
		WithTokenValidator(fakeValidator{tokens: map[string]string{adminToken: "admin-sub"}}),
		WithAdminResolver(AdminResolverFunc(func(_ context.Context, id Identity) (bool, error) {
			return id.UserID == "admin-sub", nil
		})),
		WithTargetStore(failingCreateStore{env.store}),
		WithCredSealer(env.sealer),
	)
	create := adminRouteTable("unused")[1]
	if create.method != http.MethodPost || create.path != "/api/v1/admin/targets" {
		t.Fatalf("route table changed shape: %+v", create)
	}

	rec := env.as(adminToken, create.method, create.path, create.body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body)
	}
	line := logs.onlyLineFor(t, rec)
	if !strings.Contains(asString(line["err"]), "gateway unavailable") {
		t.Fatalf("log line does not name the cause: %v", line)
	}
	logged := logs.buf.String()
	for _, secret := range []string{adminToken, adminAccessKeyID, adminSecretAccessKey} {
		if strings.Contains(logged, secret) {
			t.Fatalf("the log holds %q:\n%s", secret, logged)
		}
	}
}

// --- structure --------------------------------------------------------------

// Every 5xx this package answers goes through serverError, which is what puts
// its cause on the log line. A direct writeError/writeJSON with a 5xx status
// would answer the client and tell the operator nothing; this test is what
// stops one from being added. The only exceptions are the observer's own panic
// answer and an answer preceded by noteCause.
func TestServerErrorsGoThroughServerError(t *testing.T) {
	direct := regexp.MustCompile(`\b(writeError|writeJSON)\(\s*\w+,\s*http\.Status(InternalServerError|NotImplemented|BadGateway|ServiceUnavailable|GatewayTimeout)\b`)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(src), "\n")
		for i, l := range lines {
			if !direct.MatchString(l) {
				continue
			}
			if f == "observe.go" && strings.Contains(l, "writeError(rec,") {
				continue
			}
			if i > 0 && strings.Contains(lines[i-1], "noteCause(") {
				continue
			}
			t.Errorf("%s:%d answers 5xx without recording a cause; use serverError: %s",
				f, i+1, strings.TrimSpace(l))
		}
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func newTestSRWWrapper(t *testing.T) *keys.SRWWrapper {
	t.Helper()
	k, err := keys.GenerateSRWKey()
	if err != nil {
		t.Fatal(err)
	}
	w, err := keys.NewSRWWrapper(k)
	if err != nil {
		t.Fatal(err)
	}
	return w
}
