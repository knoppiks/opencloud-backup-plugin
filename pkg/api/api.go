// Package api holds the HTTP handlers and DTOs for the backup service, built on
// the standard library net/http ServeMux (Go 1.22+ method+path patterns) — no
// third-party router, keeping the single-static-binary goal (decisions.md
// success criterion 1).
//
// OIDC authentication middleware and the real space/backup endpoints arrive in
// Phase 2. This file wires a minimal router with a health endpoint so CI has a
// buildable, testable HTTP surface.
package api

import (
	"encoding/json"
	"net/http"
)

// Server holds handler dependencies. Dependencies are injected via constructor
// (testability rule); none are required for the Phase-1 skeleton.
type Server struct {
	mux *http.ServeMux
}

// NewServer constructs the HTTP server and registers routes.
func NewServer() *Server {
	s := &Server{mux: http.NewServeMux()}
	s.routes()
	return s
}

// Handler exposes the router for mounting or testing.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
}

// handleHealth is a liveness probe target used by the K8s Deployment.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
