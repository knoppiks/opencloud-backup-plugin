package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// While the service waits for OpenCloud it is alive, not ready, and says so
// on every route rather than leaving the connection refused.
func TestStarting(t *testing.T) {
	h := Starting()
	for _, tc := range []struct {
		method, path string
		status       int
		body         string
	}{
		{http.MethodGet, "/healthz", http.StatusOK, `"ok"`},
		{http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"starting"`},
		{http.MethodGet, "/api/v1/spaces", http.StatusServiceUnavailable, `"starting"`},
		{http.MethodPost, "/api/v1/spaces/x/backup", http.StatusServiceUnavailable, `"starting"`},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if !json.Valid(rec.Body.Bytes()) {
				t.Fatalf("body is not JSON: %q", rec.Body.String())
			}
			if got := rec.Body.String(); !strings.Contains(got, tc.body) {
				t.Fatalf("body = %q, want it to contain %s", got, tc.body)
			}
			for name, want := range responseHeaders {
				if got := rec.Header().Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}
