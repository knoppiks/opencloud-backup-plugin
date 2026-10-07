package ocversion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The body OpenCloud 8.1.0 returned in the Phase 9 spike, verbatim.
const statusPHP810 = `{
    "installed": true,
    "maintenance": false,
    "needsDbUpgrade": false,
    "version": "0.1.0.0",
    "versionstring": "0.1.0",
    "edition": "rolling",
    "productname": "OpenCloud",
    "product": "OpenCloud",
    "productversion": "8.1.0",
    "channel": ""
}`

func serve(t *testing.T, status int, body string) StatusSource {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status.php" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("status.php was sent a credential")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	// A trailing slash on OC_BASE_URL must not produce "//status.php".
	return StatusSource{BaseURL: srv.URL + "/", Client: srv.Client()}
}

func TestStatusSourceReadsProductVersion(t *testing.T) {
	info, err := serve(t, http.StatusOK, statusPHP810).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info != (Info{Version: "8.1.0", Edition: "rolling"}) {
		t.Errorf("got %+v", info)
	}
}

func TestStatusSourceErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"not ok", http.StatusServiceUnavailable, "", "HTTP 503"},
		{"not json", http.StatusOK, "<html>", "decode"},
		{"no productversion", http.StatusOK, `{"versionstring":"0.1.0"}`, "no productversion"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := serve(t, tt.status, tt.body).Fetch(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestStatusSourceUnreachable(t *testing.T) {
	src := StatusSource{BaseURL: "http://127.0.0.1:1"}
	if _, err := src.Fetch(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
}
