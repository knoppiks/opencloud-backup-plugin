package api

// Request observation: one log line per request, a request id the user can
// quote, and panic recovery (review-2026-10.md F3, F7).
//
// A handler that answers 5xx hides the cause from the client (AGENTS.md error
// rule) and hands it to serverError instead. The cause is kept on the request
// and written on the request's own log line, so every 5xx leaves exactly one
// line for the operator: the access line, raised to WARN or ERROR, carrying the
// cause and the same request id the client got in X-Request-Id.
//
// What the line never holds: the query string, any header (the bearer token is
// one), the request or response body (the Data Key at setup and target
// credentials travel there). Causes are internal errors — CS3, state, S3 —
// none of which quote a request body or a credential; noleak tests in
// observe_test.go drive the routes that carry secrets and check the log.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"time"
)

// RequestIDHeader carries the request id back to the client, so a user who
// reports a failure can quote the id the operator searches the log for.
const RequestIDHeader = "X-Request-Id"

// requestLog is what a request leaves for its log line. It lives in the
// request's context and is only touched by the goroutine serving the request.
type requestLog struct {
	id string
	// code is the API error code of a 5xx answer; cause is why it was given.
	code  string
	cause error
	// panicked holds a recovered panic and its stack.
	panicked any
	stack    []byte
}

type requestLogKey struct{}

func requestLogFrom(ctx context.Context) *requestLog {
	rl, _ := ctx.Value(requestLogKey{}).(*requestLog)
	return rl
}

// serverError answers a 5xx with a message safe for the client and records
// the cause for the request's log line. It is the only way this package
// answers 5xx (TestServerErrorsGoThroughServerError), so no cause is dropped.
// cause may be nil where the message says everything (a dependency that is not
// wired); the message is then what the log line names.
func serverError(w http.ResponseWriter, r *http.Request, status int, code, message string, cause error) {
	if cause == nil {
		cause = errors.New(message)
	}
	noteCause(r, code, cause)
	writeError(w, status, code, message)
}

// noteCause records why a request is about to be answered 5xx, for a handler
// whose answer is not the error envelope (the readiness probe).
func noteCause(r *http.Request, code string, cause error) {
	if rl := requestLogFrom(r.Context()); rl != nil {
		rl.code = code
		rl.cause = cause
	}
}

// newRequestID returns 16 hex characters from the system CSPRNG. The id is
// always minted here: an id taken from the client would let a caller choose
// what the operator's log says.
func newRequestID() string {
	var b [8]byte
	// crypto/rand.Read does not fail on supported platforms (Go 1.24+); it
	// aborts the process instead.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// statusRecorder notes what a handler wrote, for the log line and for the
// panic handler, which must not write a second header.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (rec *statusRecorder) WriteHeader(status int) {
	if rec.status == 0 {
		rec.status = status
	}
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *statusRecorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	n, err := rec.ResponseWriter.Write(b)
	rec.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (rec *statusRecorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

// observe wraps the router: request id, panic recovery and the request's log
// line.
func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rl := &requestLog{id: newRequestID()}
		w.Header().Set(RequestIDHeader, rl.id)
		rec := &statusRecorder{ResponseWriter: w}
		r = r.WithContext(context.WithValue(r.Context(), requestLogKey{}, rl))

		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					// net/http's own signal to abort the response; it is not a
					// bug and net/http does not log it either.
					panic(v)
				}
				rl.panicked, rl.stack = v, debug.Stack()
				if rec.status == 0 {
					writeError(rec, http.StatusInternalServerError, "internal_error", "internal error")
				}
			}
			s.logRequest(r, rec, rl, time.Since(started))
		}()
		next.ServeHTTP(rec, r)
	})
}

// logRequest writes the request's one log line.
func (s *Server) logRequest(r *http.Request, rec *statusRecorder, rl *requestLog, took time.Duration) {
	status := rec.status
	if status == 0 {
		// A handler that wrote nothing answered 200 with an empty body.
		status = http.StatusOK
	}
	level := requestLevel(r.URL.Path, status, rl.panicked != nil)
	if !s.logger.Enabled(r.Context(), level) {
		return
	}

	attrs := []slog.Attr{
		slog.String("request_id", rl.id),
		slog.String("method", r.Method),
		slog.String("path", loggedPath(r)),
		slog.Int("status", status),
		slog.Int64("duration_ms", took.Milliseconds()),
		slog.Int64("bytes", rec.bytes),
	}
	msg := "http request"
	if status >= http.StatusInternalServerError || rl.panicked != nil {
		msg = "http request failed"
		if rl.code != "" {
			attrs = append(attrs, slog.String("code", rl.code))
		}
		if rl.cause != nil {
			attrs = append(attrs, slog.String("err", rl.cause.Error()))
		}
		if rl.panicked != nil {
			attrs = append(attrs,
				slog.String("panic", fmt.Sprint(rl.panicked)),
				slog.String("stack", string(rl.stack)))
		}
	}
	s.logger.LogAttrs(r.Context(), level, msg, attrs...)
}

// requestLevel decides how loud a request is.
//
//   - A panic, and any 5xx but 503, is ERROR: something broke.
//   - 503 is WARN: it means "not now" — a dependency not wired or not up yet,
//     or the service shutting down — which needs attention when it lasts and
//     not otherwise.
//   - The health probes are DEBUG while they pass. The kubelet calls them every
//     few seconds; at INFO they would bury everything else.
//   - Everything else is INFO.
func requestLevel(path string, status int, panicked bool) slog.Level {
	switch {
	case panicked:
		return slog.LevelError
	case status == http.StatusServiceUnavailable:
		return slog.LevelWarn
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case isProbe(path):
		return slog.LevelDebug
	default:
		return slog.LevelInfo
	}
}

func isProbe(path string) bool { return path == "/healthz" || path == "/readyz" }

// loggedPath is the path the client asked for, without its query string: the
// query is the one part of a request line that might carry something a log
// should not hold. It is taken from the request line rather than r.URL, so a
// base path stripped before the router (BACKUPD_BASE_PATH) is still in the log.
func loggedPath(r *http.Request) string {
	if r.RequestURI != "" {
		if u, err := url.ParseRequestURI(r.RequestURI); err == nil && u.Path != "" {
			return u.EscapedPath()
		}
	}
	return r.URL.EscapedPath()
}
