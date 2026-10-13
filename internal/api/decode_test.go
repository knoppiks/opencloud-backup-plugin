package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// decodeTarget has a nested object, so unknown fields are checked at depth.
type decodeTarget struct {
	Name  string `json:"name"`
	Inner *struct {
		Value int `json:"value"`
	} `json:"inner"`
}

const decodeTestLimit = 64

func decodeForTest(t *testing.T, body string) (*httptest.ResponseRecorder, decodeTarget, bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return decodeRequestForTest(req)
}

func decodeRequestForTest(req *http.Request) (*httptest.ResponseRecorder, decodeTarget, bool) {
	rec := httptest.NewRecorder()
	var got decodeTarget
	ok := decodeJSONBody(rec, req, decodeTestLimit, &got)
	return rec, got, ok
}

func TestDeclaresJSON(t *testing.T) {
	cases := []struct {
		contentType []string
		want        bool
	}{
		{[]string{"application/json"}, true},
		{[]string{"application/json; charset=utf-8"}, true},
		{[]string{"Application/JSON; Charset=UTF-8"}, true},
		{[]string{`application/json;charset="utf-8"`}, true},
		// application/json defines no parameters; an unknown one changes nothing.
		{[]string{"application/json; version=1"}, true},

		{nil, false},
		{[]string{""}, false},
		{[]string{"text/plain"}, false},
		{[]string{"text/plain; charset=utf-8"}, false},
		{[]string{"application/x-www-form-urlencoded"}, false},
		{[]string{"multipart/form-data; boundary=x"}, false},
		{[]string{"application/json; charset=iso-8859-1"}, false},
		{[]string{"application/json; charset=utf-16"}, false},
		{[]string{"application/merge-patch+json"}, false},
		{[]string{"application/jsonp"}, false},
		{[]string{"application/json; charset"}, false},
		{[]string{"application/json", "application/json"}, false},
		{[]string{"application/json", "text/plain"}, false},
	}
	for _, tc := range cases {
		h := http.Header{}
		for _, v := range tc.contentType {
			h.Add("Content-Type", v)
		}
		if got := declaresJSON(h); got != tc.want {
			t.Errorf("declaresJSON(%q) = %v, want %v", tc.contentType, got, tc.want)
		}
	}
}

// failingReader fails the test when the body is read at all.
type failingReader struct{ t *testing.T }

func (f failingReader) Read([]byte) (int, error) {
	f.t.Error("the body was read although its type was refused")
	return 0, io.EOF
}

// A body not declared as JSON is refused before a byte of it is read (#92).
func TestDecodeJSONBody_RefusesBodiesNotDeclaredAsJSON(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded", "application/json; charset=iso-8859-1"} {
		t.Run(contentType, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", failingReader{t})
			if contentType != "" {
				req.Header.Set("Content-Type", contentType)
			}
			rec, _, ok := decodeRequestForTest(req)
			if ok {
				t.Fatal("accepted")
			}
			assertRefusal(t, rec, http.StatusUnsupportedMediaType, "unsupported_media_type", msgBodyNotJSON)
		})
	}
}

func TestDecodeJSONBody_AcceptsOneKnownValue(t *testing.T) {
	cases := map[string]string{
		"object":                   `{"name":"a","inner":{"value":1}}`,
		"surrounding whitespace":   " \n\t{\"name\":\"a\"} \r\n",
		"fields omitted":           `{}`,
		"exactly at the limit":     `{"name":"` + strings.Repeat("a", decodeTestLimit-11) + `"}`,
		"whitespace up to limit":   `{"name":"a"}` + strings.Repeat(" ", decodeTestLimit-12),
		"null for a nested object": `{"name":"a","inner":null}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if len(body) > decodeTestLimit {
				t.Fatalf("test body is %d bytes, over the limit", len(body))
			}
			rec, _, ok := decodeForTest(t, body)
			if !ok {
				t.Fatalf("refused: %d %s", rec.Code, rec.Body)
			}
			if rec.Body.Len() != 0 || rec.Code != http.StatusOK {
				t.Fatalf("an accepted body must leave the answer to the handler: %d %q", rec.Code, rec.Body)
			}
		})
	}
}

func TestDecodeJSONBody_FillsTheTarget(t *testing.T) {
	_, got, ok := decodeForTest(t, `{"name":"a","inner":{"value":7}}`)
	if !ok || got.Name != "a" || got.Inner == nil || got.Inner.Value != 7 {
		t.Fatalf("decoded %+v (ok %v)", got, ok)
	}
}

func TestDecodeJSONBody_RefusesMalformedBodies(t *testing.T) {
	cases := map[string]string{
		"empty":                ``,
		"whitespace only":      `   `,
		"broken json":          `{"name":`,
		"unknown field":        `{"name":"a","nam":"b"}`,
		"unknown nested field": `{"inner":{"value":1,"valeu":2}}`,
		"second value":         `{"name":"a"}{"name":"b"}`,
		"trailing garbage":     `{"name":"a"} x`,
		"trailing bracket":     `{"name":"a"}}`,
		"wrong type":           `{"name":1}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec, _, ok := decodeForTest(t, body)
			if ok {
				t.Fatal("accepted")
			}
			assertRefusal(t, rec, http.StatusBadRequest, "bad_request", msgMalformedBody)
		})
	}
}

func TestDecodeJSONBody_RefusesBodiesOverTheLimit(t *testing.T) {
	cases := map[string]string{
		"value over the limit": `{"name":"` + strings.Repeat("a", decodeTestLimit) + `"}`,
		// A complete value inside the limit does not let the rest through.
		"trailing bytes over the limit": `{"name":"a"}` + strings.Repeat(" ", decodeTestLimit),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec, _, ok := decodeForTest(t, body)
			if ok {
				t.Fatal("accepted")
			}
			assertRefusal(t, rec, http.StatusRequestEntityTooLarge, "payload_too_large", msgBodyTooLarge)
		})
	}
}

// The refusal never repeats the input: a body can hold a Data Key or target
// credentials.
func TestDecodeJSONBody_RefusalDoesNotEchoTheInput(t *testing.T) {
	const marker = "do-not-echo-this"
	for _, body := range []string{
		`{"` + marker + `":1}`,
		`{"name":` + marker + `}`,
		`{"name":"` + marker + `"}{}`,
	} {
		rec, _, _ := decodeForTest(t, body)
		if strings.Contains(rec.Body.String(), marker) {
			t.Fatalf("refusal of %q echoes the input: %s", body, rec.Body)
		}
	}
}

func assertRefusal(t *testing.T, rec *httptest.ResponseRecorder, status int, code, message string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body)
	}
	var body errorBody
	decodeBody(t, rec.Body, &body)
	if body.Error.Code != code || body.Error.Message != message {
		t.Fatalf("error = %+v, want %s / %q", body.Error, code, message)
	}
}

// bodyRoute is one route that takes a JSON body, with a server on which the
// request reaches the body: authorised, its dependencies wired and the Space
// in the state the route needs.
type bodyRoute struct {
	method, path, token string
	limit               int64
	server              func(t *testing.T) *Server
}

func bodyRoutes() map[string]bodyRoute {
	backupSrv := func(t *testing.T) *Server { return newBackupTestEnv(t).srv }
	adminSrv := func(t *testing.T) *Server {
		env := newAdminTestEnv(t)
		env.seedTarget(t, "t1")
		return env.srv
	}
	return map[string]bodyRoute{
		"key setup": {
			http.MethodPost, "/api/v1/spaces/space-alice/backup/setup", "alice-tok", maxKeyRequestBytes,
			func(t *testing.T) *Server { return newKeyTestEnv(t).srv },
		},
		"recovery key rotation": {
			http.MethodPost, "/api/v1/spaces/space-alice/backup/recovery-key/rotate", "alice-tok", maxKeyRequestBytes,
			func(t *testing.T) *Server {
				env := newKeyTestEnv(t)
				body, _, _ := clientSetup(t)
				if rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/backup/setup", "alice-tok", body); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
					t.Fatalf("setup = %d: %s", rec.Code, rec.Body)
				}
				return env.srv
			},
		},
		"put backup config": {
			http.MethodPut, "/api/v1/spaces/space-alice/backup/config", "alice-tok", maxConfigRequestBytes, backupSrv,
		},
		"patch backup config": {
			http.MethodPatch, "/api/v1/spaces/space-alice/backup/config", "alice-tok", maxConfigRequestBytes, backupSrv,
		},
		"put schedule": {
			http.MethodPut, "/api/v1/spaces/space-alice/backup/schedule", "alice-tok", maxScheduleRequestBytes,
			func(t *testing.T) *Server { return newScheduleEnv(t, fakeAdvisor{}).srv },
		},
		"restore": {
			http.MethodPost, "/api/v1/spaces/space-alice/restore", "alice-tok", maxRestoreRequestBytes,
			func(t *testing.T) *Server { return newRestoreTestEnv(t).srv },
		},
		"admin create target": {
			http.MethodPost, "/api/v1/admin/targets", adminToken, maxAdminRequestBytes, adminSrv,
		},
		"admin check target": {
			http.MethodPost, "/api/v1/admin/targets/check", adminToken, maxAdminRequestBytes, adminSrv,
		},
		"admin update target": {
			http.MethodPut, "/api/v1/admin/targets/t1", adminToken, maxAdminRequestBytes, adminSrv,
		},
		"admin replace grants": {
			http.MethodPut, "/api/v1/admin/targets/t1/grants", adminToken, maxAdminRequestBytes, adminSrv,
		},
	}
}

// Every route that takes a body reads it through decodeJSONBody. The message
// tells the helper's refusal from the handler's own validation, which would
// answer 400 too.
func TestBodyRoutesReadTheirBodyStrictly(t *testing.T) {
	for name, route := range bodyRoutes() {
		bodies := map[string]struct {
			body          []byte
			status        int
			code, message string
		}{
			"unknown field": {[]byte(`{"unexpected_field":1}`), http.StatusBadRequest, "bad_request", msgMalformedBody},
			"second value":  {[]byte(`{}{}`), http.StatusBadRequest, "bad_request", msgMalformedBody},
			"empty":         {[]byte{}, http.StatusBadRequest, "bad_request", msgMalformedBody},
			"over the limit": {
				append([]byte(`{}`), bytes.Repeat([]byte(" "), int(route.limit))...),
				http.StatusRequestEntityTooLarge, "payload_too_large", msgBodyTooLarge,
			},
		}
		for bodyName, tc := range bodies {
			t.Run(name+"/"+bodyName, func(t *testing.T) {
				srv := route.server(t)
				rec := doJSON(srv, route.method, route.path, route.token, tc.body)
				assertRefusal(t, rec, tc.status, tc.code, tc.message)
			})
		}
		// A well-formed body that is not declared as JSON (#92).
		for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
			t.Run(name+"/declared as "+strconv.Quote(contentType), func(t *testing.T) {
				srv := route.server(t)
				rec := doBody(srv, route.method, route.path, route.token, contentType, []byte(`{}`))
				assertRefusal(t, rec, http.StatusUnsupportedMediaType, "unsupported_media_type", msgBodyNotJSON)
			})
		}
	}
}

// The routes table above is every route with a body: a route added later
// without a row would not have its body reading checked.
func TestBodyRoutesListsEveryRouteWithABody(t *testing.T) {
	src, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	registered := regexp.MustCompile(`s\.mux\.Handle\("(POST|PUT|PATCH) ([^"]+)"`).FindAllStringSubmatch(string(src), -1)
	// Routes that take no body.
	noBody := map[string]bool{"POST /api/v1/spaces/{id}/backup/run": true}

	listed := map[string]bool{}
	for _, r := range bodyRoutes() {
		listed[r.method+" "+r.path] = true
	}
	for _, m := range registered {
		pattern := m[1] + " " + m[2]
		if noBody[pattern] {
			continue
		}
		concrete := strings.NewReplacer("{id}", "space-alice").Replace(pattern)
		if strings.Contains(pattern, "/admin/targets/{id}") {
			concrete = strings.NewReplacer("{id}", "t1").Replace(pattern)
		}
		if !listed[concrete] {
			t.Errorf("%s takes a body but is not in bodyRoutes", pattern)
		}
	}
}

// Nothing in this package reads a request body but decodeJSONBody, so the
// rules cannot drift apart again (#90).
func TestRequestBodiesGoThroughDecodeJSONBody(t *testing.T) {
	readsBody := regexp.MustCompile(`\br\.Body\b`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "decode.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, l := range strings.Split(string(src), "\n") {
			if readsBody.MatchString(l) {
				t.Errorf("%s:%d reads the request body directly; use decodeJSONBody: %s",
					f, i+1, strings.TrimSpace(l))
			}
		}
	}
}

func TestEveryResponseCarriesTheResponseHeaders(t *testing.T) {
	srv := NewServer()
	requests := map[string]*http.Request{
		"probe":              httptest.NewRequest(http.MethodGet, "/healthz", nil),
		"unauthenticated":    httptest.NewRequest(http.MethodGet, "/api/v1/spaces", nil),
		"unknown path":       httptest.NewRequest(http.MethodGet, "/nowhere", nil),
		"method not allowed": httptest.NewRequest(http.MethodDelete, "/healthz", nil),
	}
	for name, req := range requests {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			assertResponseHeaders(t, rec)
		})
	}
}

// A panic is answered by the recovery in observe; the headers are set before
// it, so they are on that answer too.
func TestPanicAnswerCarriesTheResponseHeaders(t *testing.T) {
	srv := NewServer()
	srv.mux.HandleFunc("GET /panic", func(http.ResponseWriter, *http.Request) { panic("boom") })
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	assertResponseHeaders(t, rec)
}

func assertResponseHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for name, want := range map[string]string{
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q (status %d)", name, got, want, rec.Code)
		}
	}
}
