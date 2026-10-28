package api

import (
	"errors"
	"net/http"
)

// errStarting is the cause of every 503 Starting answers.
var errStarting = errors.New("the service is starting and waiting for OpenCloud")

// Starting is what the listener answers while the service waits for OpenCloud
// at startup (#152): alive, not ready, and every other route unavailable for
// now. The listener is up before the wait so that a probe tells "starting"
// from "dead" — /healthz succeeds, /readyz does not — instead of finding
// nothing there.
func Starting() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		noteCause(r, "starting", errStarting)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "starting"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		serverError(w, r, http.StatusServiceUnavailable, "starting",
			"The backup service is starting and waiting for OpenCloud. Try again shortly.", errStarting)
	})
	return WithResponseHeaders(mux)
}
