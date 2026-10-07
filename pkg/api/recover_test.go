package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoverPanicsAnswers500AndLogsTheCause(t *testing.T) {
	var logs bytes.Buffer
	h := RecoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("handler bug")
	}), slog.New(slog.NewTextHandler(&logs, nil)))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/spaces?token=hidden", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "handler bug") {
		t.Fatalf("the panic reached the client: %s", rec.Body.String())
	}
	got := logs.String()
	if !strings.Contains(got, "handler bug") || !strings.Contains(got, "/api/v1/spaces") {
		t.Fatalf("the panic was not logged:\n%s", got)
	}
	if strings.Contains(got, "hidden") {
		t.Fatalf("the query string was logged:\n%s", got)
	}
}

func TestRecoverPanicsKeepsErrAbortHandler(t *testing.T) {
	h := RecoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}), nil)

	defer func() {
		if v := recover(); v != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler re-raised", v)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestRecoverPanicsPassesNormalRequestsThrough(t *testing.T) {
	h := RecoverPanics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d", rec.Code)
	}
}
