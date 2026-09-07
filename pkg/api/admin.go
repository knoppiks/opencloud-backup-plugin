// Admin detection and the admin-only route gate (phase-2 deliverable 1;
// decisions.md #13/#15).
//
// Admin status grants ONLY target/grant management — never plaintext, never
// cross-space backup access (decisions.md #15). This file resolves whether the
// caller is an OpenCloud admin and gates /api/v1/admin/* behind that.
//
// Mechanism (pinned by the admin-role spike, phase-0-findings.md): the OIDC
// access token carries no role claim on OpenCloud 7.3.0, so admin status is
// resolved via the graph API — GET /graph/v1.0/me?$expand=appRoleAssignments —
// mapping the Admin app-role id. A config allow-list resolver is provided as the
// decisions.md #13 fallback.
package api

import (
	"context"
	"net/http"
	"strings"
)

type isAdminCtxKey struct{}

// IsAdminFrom reports whether the request context is flagged as an admin caller.
func IsAdminFrom(ctx context.Context) bool {
	v, _ := ctx.Value(isAdminCtxKey{}).(bool)
	return v
}

func withIsAdmin(ctx context.Context, isAdmin bool) context.Context {
	return context.WithValue(ctx, isAdminCtxKey{}, isAdmin)
}

// AdminResolver decides whether an authenticated caller is an OpenCloud admin.
// Implementations must never grant data powers — the flag gates target/grant
// management only (decisions.md #15).
type AdminResolver interface {
	IsAdmin(ctx context.Context, id Identity) (bool, error)
}

// AdminResolverFunc adapts a function to AdminResolver.
type AdminResolverFunc func(ctx context.Context, id Identity) (bool, error)

// IsAdmin implements AdminResolver.
func (f AdminResolverFunc) IsAdmin(ctx context.Context, id Identity) (bool, error) {
	return f(ctx, id)
}

// --- graph-based resolver (primary) ----------------------------------------

// GraphAdminResolver resolves admin status via the OpenCloud graph API using the
// caller's own bearer token. The default admin app-role id matches OpenCloud
// 7.3.0's static role bundle (phase-0-findings.md) and is overridable per
// deployment.
type GraphAdminResolver struct {
	// AdminAppRoleID is the graph appRoleId that denotes an admin.
	AdminAppRoleID string
	// graph fetches the caller's /me document.
	graph graphMeFetcher
}

// DefaultAdminAppRoleID is the OpenCloud 7.3.0 "Admin" app-role id
// (phase-0-findings.md, admin-role spike).
const DefaultAdminAppRoleID = "71881883-1768-46bd-a24d-a356a2afdf7f"

// NewGraphAdminResolver constructs a graph resolver. If adminAppRoleID is empty
// the pinned default is used.
func NewGraphAdminResolver(baseURL, adminAppRoleID string, client *http.Client) *GraphAdminResolver {
	if adminAppRoleID == "" {
		adminAppRoleID = DefaultAdminAppRoleID
	}
	return &GraphAdminResolver{
		AdminAppRoleID: adminAppRoleID,
		graph:          newGraphMeFetcher(baseURL, client),
	}
}

// IsAdmin calls the graph API as the caller and maps their app-role assignments
// to admin status. A non-admin (or a user without the admin role) yields false;
// transport/graph errors are returned so the caller can decide (fail-closed).
func (g *GraphAdminResolver) IsAdmin(ctx context.Context, id Identity) (bool, error) {
	me, err := g.graph.fetch(ctx, "graph admin resolve", id, expandAppRoles)
	if err != nil {
		return false, err
	}
	for _, a := range me.AppRoleAssignments {
		if a.AppRoleID == g.AdminAppRoleID {
			return true, nil
		}
	}
	return false, nil
}

// --- allow-list resolver (fallback, decisions.md #13) ----------------------

// AllowlistAdminResolver treats a fixed set of subject ids as admins. This is
// the documented fallback for instances where graph detection is undesirable.
type AllowlistAdminResolver struct {
	subs map[string]struct{}
}

// NewAllowlistAdminResolver builds an allow-list resolver from subject ids.
func NewAllowlistAdminResolver(subjects []string) *AllowlistAdminResolver {
	m := make(map[string]struct{}, len(subjects))
	for _, s := range subjects {
		if s = strings.TrimSpace(s); s != "" {
			m[s] = struct{}{}
		}
	}
	return &AllowlistAdminResolver{subs: m}
}

// IsAdmin reports whether the caller's subject is in the allow-list.
func (a *AllowlistAdminResolver) IsAdmin(_ context.Context, id Identity) (bool, error) {
	_, ok := a.subs[id.Subject]
	return ok, nil
}

// --- middleware ------------------------------------------------------------

// ResolveAdmin is middleware that resolves the caller's admin status and stores
// it in the request context. It must run after Authenticate. A resolver error
// fails closed (isAdmin=false) rather than leaking internal details; the flag is
// UX/authorization metadata, and admin routes are additionally gated by
// RequireAdmin.
func (s *Server) ResolveAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := IdentityFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing identity")
			return
		}
		isAdmin := false
		if s.adminResolver != nil {
			if v, err := s.adminResolver.IsAdmin(r.Context(), id); err == nil {
				isAdmin = v
			}
		}
		next.ServeHTTP(w, r.WithContext(withIsAdmin(r.Context(), isAdmin)))
	})
}

// RequireAdmin is middleware that 403s non-admin callers. It must run after
// ResolveAdmin (and Authenticate).
func (s *Server) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsAdminFrom(r.Context()) {
			writeError(w, http.StatusForbidden, "forbidden", "admin role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
