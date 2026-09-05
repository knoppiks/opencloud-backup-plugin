// Package api holds the HTTP handlers and DTOs for the backup service, built on
// the standard library net/http ServeMux (Go 1.22+ method+path patterns) — no
// third-party router, keeping the single-static-binary goal (decisions.md
// success criterion 1).
//
// Phase 2 wires the first real end-to-end slice: browser token -> OIDC
// middleware -> CS3 gateway -> JSON. Authorization is enforced server-side: a
// user sees only Spaces they are a member of and only backup targets granted to
// them (phase-2 deliverable 4); client-supplied ids are never trusted.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/targets"
)

// Server holds handler dependencies. Dependencies are injected via constructor
// options (testability rule); the zero Server is not usable — use NewServer.
type Server struct {
	mux *http.ServeMux

	validator     TokenValidator
	adminResolver AdminResolver
	spaces        cs3.SpaceReader
	authorizer    targets.Authorizer

	// keyStore persists wrapped Data Keys; srw adds the server-side wrap at
	// setup time. Both hold ciphertext / server-held key material only — no
	// plaintext DK or RK is ever stored or returned (decisions.md, Phase 3).
	keyStore keys.Store
	srw      srwWrapper

	// ready reports readiness for GET /readyz; defaults to always-ready.
	ready func(context.Context) error
}

// Option configures a Server.
type Option func(*Server)

// WithTokenValidator sets the OIDC token validator used by Authenticate.
func WithTokenValidator(v TokenValidator) Option { return func(s *Server) { s.validator = v } }

// WithAdminResolver sets the admin-status resolver used by ResolveAdmin.
func WithAdminResolver(r AdminResolver) Option { return func(s *Server) { s.adminResolver = r } }

// WithSpaceReader sets the CS3 space reader backing GET /spaces.
func WithSpaceReader(r cs3.SpaceReader) Option { return func(s *Server) { s.spaces = r } }

// WithAuthorizer sets the target authorizer backing GET /targets.
func WithAuthorizer(a targets.Authorizer) Option { return func(s *Server) { s.authorizer = a } }

// WithKeyStore sets the wrapped-key store backing the backup key endpoints.
func WithKeyStore(st keys.Store) Option { return func(s *Server) { s.keyStore = st } }

// WithSRWWrapper sets the Server Runtime Wrap holder used at key setup. It is
// satisfied by *keys.SRWWrapper.
func WithSRWWrapper(w srwWrapper) Option { return func(s *Server) { s.srw = w } }

// WithReadiness sets the readiness probe for GET /readyz.
func WithReadiness(fn func(context.Context) error) Option {
	return func(s *Server) { s.ready = fn }
}

// NewServer constructs the HTTP server and registers routes.
func NewServer(opts ...Option) *Server {
	s := &Server{mux: http.NewServeMux()}
	for _, opt := range opts {
		opt(s)
	}
	if s.ready == nil {
		s.ready = func(context.Context) error { return nil }
	}
	s.routes()
	return s
}

// Handler exposes the router for mounting or testing.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	// Unauthenticated probes.
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /readyz", s.handleReady)

	// Authenticated user API. Authenticate is a no-op-safe gate: without a
	// validator configured, protected routes always 401 (fail closed).
	authed := func(h http.HandlerFunc) http.Handler {
		return s.Authenticate(h)
	}
	s.mux.Handle("GET /api/v1/spaces", authed(s.handleListSpaces))
	s.mux.Handle("GET /api/v1/targets", authed(s.handleListTargets))

	// Backup key ceremony (Phase 3). All three are space-scoped and enforce CS3
	// membership server-side; none ever returns plaintext key material.
	s.mux.Handle("POST /api/v1/spaces/{id}/backup/setup", authed(s.handleKeySetup))
	s.mux.Handle("GET /api/v1/spaces/{id}/backup/keystatus", authed(s.handleKeyStatus))
	s.mux.Handle("GET /api/v1/spaces/{id}/backup/recovery-envelope", authed(s.handleRecoveryEnvelope))

	// Admin API scaffold: Authenticate -> ResolveAdmin -> RequireAdmin. The
	// concrete admin target/grant endpoints are added in the target-store phase;
	// Phase 2 provides the middleware chain and a 404-under-gate default so the
	// gate itself is testable (admin -> passes gate, non-admin -> 403).
	adminGate := s.Authenticate(s.ResolveAdmin(s.RequireAdmin(http.HandlerFunc(s.handleAdminNotImplemented))))
	s.mux.Handle("/api/v1/admin/", adminGate)
}

// handleHealth is a liveness probe target used by the K8s Deployment.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady is a readiness probe: 200 when dependencies are reachable, else
// 503. It never leaks dependency error details to the caller.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.ready(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// spaceDTO is the JSON projection of a Space for GET /spaces. It deliberately
// omits CS3-internal detail beyond what the UI needs; membership is not exposed
// to the client (it is used server-side for authorization).
type spaceDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// handleListSpaces returns the spaces the authenticated user may back up. A user
// may only see Spaces they are a member of, enforced against CS3 membership, not
// client input (phase-2 deliverable 4).
func (s *Server) handleListSpaces(w http.ResponseWriter, r *http.Request) {
	id, ok := IdentityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing identity")
		return
	}
	if s.spaces == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "space backend not configured")
		return
	}

	all, err := s.spaces.ListSpaces(r.Context())
	if err != nil {
		// Never leak CS3 detail (phase-2 deliverable 3, exit criterion 2).
		writeError(w, http.StatusBadGateway, "upstream_error", "could not list spaces")
		return
	}

	out := make([]spaceDTO, 0, len(all))
	for _, sp := range all {
		if !isMember(sp, id.Subject) {
			continue
		}
		out = append(out, spaceDTO{ID: sp.ID, Name: sp.Name, Type: sp.Type})
	}
	writeJSON(w, http.StatusOK, map[string]any{"spaces": out})
}

// isMember reports whether subject may see space: they own it or appear in the
// membership grants (decisions.md #6/#7; phase-2 deliverable 4). Enforced
// server-side against CS3 data, never client input.
func isMember(space cs3.Space, subject string) bool {
	if subject == "" {
		return false
	}
	if space.Owner == subject {
		return true
	}
	if _, ok := space.Members[subject]; ok {
		return true
	}
	return false
}

// targetDTO is the least-disclosure projection for GET /targets: only what the
// UI needs to pick a target — never credentials, endpoint, bucket, or region
// (decisions.md #12/#14; phase-2 deliverable 3).
type targetDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// handleListTargets returns the backup targets granted to the authenticated
// user. Grant checks are server-side via the authorizer; client input is never
// trusted (phase-2 deliverable 4).
func (s *Server) handleListTargets(w http.ResponseWriter, r *http.Request) {
	id, ok := IdentityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing identity")
		return
	}
	if s.authorizer == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "target backend not configured")
		return
	}

	// The set of spaces the user belongs to feeds space-scoped grants. We derive
	// it from CS3 membership (never client input); when no space reader is wired
	// only all-users / per-user grants apply.
	spaceIDs, err := s.memberSpaceIDs(r.Context(), id.Subject)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", "could not resolve spaces")
		return
	}

	views, err := s.authorizer.VisibleTargets(r.Context(), id.Subject, spaceIDs)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", "could not list targets")
		return
	}

	out := make([]targetDTO, 0, len(views))
	for _, v := range views {
		out = append(out, targetDTO{ID: v.ID, Name: v.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": out})
}

// memberSpaceIDs returns the ids of spaces subject belongs to, or an empty slice
// if no space reader is configured. Used only to evaluate space-scoped grants.
func (s *Server) memberSpaceIDs(ctx context.Context, subject string) ([]string, error) {
	if s.spaces == nil {
		return nil, nil
	}
	all, err := s.spaces.ListSpaces(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, sp := range all {
		if isMember(sp, subject) {
			ids = append(ids, sp.ID)
		}
	}
	return ids, nil
}

// handleAdminNotImplemented is the placeholder body behind the admin gate. It is
// only ever reached by an authenticated admin; concrete endpoints land with the
// target store. It exists so the gate (403 for non-admins) is testable now.
func (s *Server) handleAdminNotImplemented(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "admin endpoint not implemented")
}

// --- response helpers ------------------------------------------------------

// errorBody is the consistent error envelope. It never contains internal error
// detail (phase-2 deliverable 3; AGENTS.md error rule).
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var b errorBody
	b.Error.Code = code
	b.Error.Message = message
	writeJSON(w, status, b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ErrNotConfigured is returned by constructors when a required dependency is
// missing. Exported for callers that wire the server.
var ErrNotConfigured = errors.New("api: server dependency not configured")
