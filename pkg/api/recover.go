package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// RecoverPanics answers a request whose handler panicked with a plain 500 and
// logs the panic with its stack. net/http would contain the panic too, but it
// drops the connection without an answer and logs outside the service's
// structured log (review-2026-10.md F7). The client learns nothing beyond
// "internal error"; the cause stays in the operator's log.
//
// http.ErrAbortHandler keeps its meaning: it is re-raised for net/http.
func RecoverPanics(next http.Handler, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(v)
			}
			// The path without its query: a query string is the one part of a
			// request line that might carry something a log should not hold.
			logger.Error("HTTP handler panicked",
				"method", r.Method, "path", r.URL.Path,
				"panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		}()
		next.ServeHTTP(w, r)
	})
}
