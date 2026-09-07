// Role-gated space authorization (remediation R3).
//
// Presence in a Space's grants is not authority: a grant carries a permission
// set, may have lapsed, and may belong to a group rather than a user. Every
// space-scoped route therefore states the minimum role it needs and is refused
// below it.
//
// Role table (phase-2-auth-spaces.md):
//
//	viewer   status, schedule, runs, notifications, config (read), snapshots,
//	         recovery-envelope retrieval, restore into the Space (decisions.md #7)
//	editor   run backup now, change config/schedule/retention
//	manager  key setup, Recovery-Key rotation
//
// Group grants are resolved against the graph API with the caller's own bearer
// token, and only when the caller's direct grant does not already reach the
// required role — so the common case costs no upstream call.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"opencloud-backup-plugin/pkg/cs3"
)

// GroupResolver resolves the group ids an authenticated caller belongs to.
// Implementations must derive them from the identity provider or OpenCloud —
// never from client input.
type GroupResolver interface {
	Groups(ctx context.Context, id Identity) ([]string, error)
}

// GroupResolverFunc adapts a function to GroupResolver.
type GroupResolverFunc func(ctx context.Context, id Identity) ([]string, error)

// Groups implements GroupResolver.
func (f GroupResolverFunc) Groups(ctx context.Context, id Identity) ([]string, error) {
	return f(ctx, id)
}

// GraphGroupResolver resolves group membership via the OpenCloud graph API
// (GET /graph/v1.0/me?$expand=memberOf) using the caller's own bearer token.
// OpenCloud 7.3.0 exposes no groups claim on the access token and has no
// /me/memberOf route, so the expanded /me document is the supported source.
type GraphGroupResolver struct {
	graph graphMeFetcher
}

// NewGraphGroupResolver builds a graph-backed group resolver.
func NewGraphGroupResolver(baseURL string, client *http.Client) *GraphGroupResolver {
	return &GraphGroupResolver{graph: newGraphMeFetcher(baseURL, client)}
}

// Groups returns the caller's group ids.
func (g *GraphGroupResolver) Groups(ctx context.Context, id Identity) ([]string, error) {
	me, err := g.graph.fetch(ctx, "graph group resolve", id, expandMemberOf)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(me.MemberOf))
	for _, grp := range me.MemberOf {
		if grp.ID != "" {
			out = append(out, grp.ID)
		}
	}
	return out, nil
}

// errGroupsUnavailable is returned when a Space's group grants would have to be
// consulted but no resolver is wired. Failing here is deliberate: silently
// treating the caller as group-less would deny a legitimate member, and
// silently treating them as a member would grant one.
var errGroupsUnavailable = errors.New("api: group resolution not configured")

// --- per-request access checker --------------------------------------------

type accessCtxKey struct{}

// access answers "may this caller do X on this Space" for the duration of one
// request, memoising the Space list and the caller's groups so a handler pays
// for each upstream call at most once (R3 task 5).
type access struct {
	spaces cs3.SpaceReader
	groups GroupResolver
	id     Identity
	now    time.Time

	spaceList []cs3.Space
	spaceErr  error
	spaceDone bool
	groupList []string
	groupErr  error
	groupDone bool
}

// accessFrom returns the request's access checker.
func accessFrom(ctx context.Context) (*access, bool) {
	a, ok := ctx.Value(accessCtxKey{}).(*access)
	return a, ok
}

// withAccess is middleware that attaches a per-request access checker. It must
// run after Authenticate.
func (s *Server) withAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := IdentityFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing identity")
			return
		}
		a := &access{spaces: s.spaces, groups: s.groupResolver, id: id, now: s.clock()}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accessCtxKey{}, a)))
	})
}

// listSpaces returns the CS3 space list, fetching it at most once per request.
func (a *access) listSpaces(ctx context.Context) ([]cs3.Space, error) {
	if !a.spaceDone {
		a.spaceDone = true
		if a.spaces == nil {
			a.spaceErr = ErrNotConfigured
		} else {
			a.spaceList, a.spaceErr = a.spaces.ListSpaces(ctx)
		}
	}
	return a.spaceList, a.spaceErr
}

// callerGroups returns the caller's group ids, resolving them at most once per
// request.
func (a *access) callerGroups(ctx context.Context) ([]string, error) {
	if !a.groupDone {
		a.groupDone = true
		if a.groups == nil {
			a.groupErr = errGroupsUnavailable
		} else {
			a.groupList, a.groupErr = a.groups.Groups(ctx, a.id)
		}
	}
	return a.groupList, a.groupErr
}

// permits reports whether the caller reaches min on sp. The caller's direct
// grant is evaluated first; group grants are consulted only when it falls short
// and the Space actually has one.
func (a *access) permits(ctx context.Context, sp cs3.Space, min cs3.Role) (bool, error) {
	if sp.RoleFor(a.id.Subject, nil, a.now) >= min {
		return true, nil
	}
	if !sp.GroupGrants() {
		return false, nil
	}
	groups, err := a.callerGroups(ctx)
	if err != nil {
		return false, err
	}
	return sp.RoleFor(a.id.Subject, groups, a.now) >= min, nil
}

// memberSpaceIDs returns the ids of the Spaces the caller can at least view.
// Used to evaluate space-scoped target grants.
func (a *access) memberSpaceIDs(ctx context.Context) ([]string, error) {
	all, err := a.listSpaces(ctx)
	if errors.Is(err, ErrNotConfigured) {
		// No space reader wired: only all-users / per-user target grants apply.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, sp := range all {
		ok, err := a.permits(ctx, sp, cs3.RoleViewer)
		if err != nil {
			return nil, err
		}
		if ok {
			ids = append(ids, sp.ID)
		}
	}
	return ids, nil
}

// --- handler entry points --------------------------------------------------

// requireRole resolves the caller, the path's space id, and enforces that the
// caller holds at least min on that Space. It writes the error response and
// returns ok=false when access is denied.
//
// A Space the caller may not reach and a Space that does not exist produce the
// same 403, so membership is not enumerable. An insufficient role produces the
// same 403 too: telling a viewer that they are "only a viewer" of a Space they
// can see is harmless, but keeping one branch keeps the handler honest.
func (s *Server) requireRole(w http.ResponseWriter, r *http.Request, min cs3.Role) (Identity, string, bool) {
	id, ok := IdentityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing identity")
		return Identity{}, "", false
	}
	spaceID := r.PathValue("id")
	if spaceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing space id")
		return Identity{}, "", false
	}
	a, ok := accessFrom(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "authorization unavailable")
		return Identity{}, "", false
	}

	all, err := a.listSpaces(r.Context())
	if errors.Is(err, ErrNotConfigured) {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "space backend not configured")
		return Identity{}, "", false
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", "could not verify space membership")
		return Identity{}, "", false
	}

	for _, sp := range all {
		if sp.ID != spaceID {
			continue
		}
		allowed, err := a.permits(r.Context(), sp, min)
		if err != nil {
			writeAccessError(w, err)
			return Identity{}, "", false
		}
		if allowed {
			return id, spaceID, true
		}
		break
	}
	writeError(w, http.StatusForbidden, "forbidden", forbiddenMessage(min))
	return Identity{}, "", false
}

// writeAccessError maps an authorization-check failure onto a response without
// leaking upstream detail. A missing group resolver is a deployment fault (503),
// a failed lookup an upstream one (502).
func writeAccessError(w http.ResponseWriter, err error) {
	if errors.Is(err, errGroupsUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "group membership cannot be verified")
		return
	}
	writeError(w, http.StatusBadGateway, "upstream_error", "could not verify space membership")
}

// forbiddenMessage names the role a refused caller would have needed. It never
// reveals whether the Space exists or who its members are.
func forbiddenMessage(min cs3.Role) string {
	if min <= cs3.RoleViewer {
		return "not a member of this space"
	}
	return fmt.Sprintf("requires the %s role on this space", min)
}
