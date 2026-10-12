// Space membership as an authority level, not a yes/no (remediation R3).
//
// A CS3 space carries its grants in the Opaque map. The shape is pinned against
// OpenCloud 7.3.0 by TestIntegration_SpaceGrantsShape:
//
//	opaque["grants"]             = {"<principal>": <provider.ResourcePermissions>, ...}
//	opaque["groups"]             = {"<principal>": {}, ...}   // which principals are groups
//	opaque["grants_expirations"] = {"<principal>": {"seconds": <unix>}, ...}
//
// The grant value is a *permission set*, not a role name — reva does not
// serialize the display role. Roles are therefore derived from capabilities:
// whoever may change grants is a manager, whoever may write is an editor,
// whoever may read is a viewer. Deriving from capabilities rather than matching
// an exact permission set keeps the mapping stable when reva adds a permission.
package cs3

import (
	"encoding/json"
	"time"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	types "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
)

// Role is a principal's authority on a Space, ordered so that a numerically
// greater role subsumes a lesser one. Handlers compare with >=.
type Role int

const (
	// RoleNone means no grant at all: the principal is not a member.
	RoleNone Role = iota
	// RoleViewer may read the Space.
	RoleViewer
	// RoleEditor may write into the Space.
	RoleEditor
	// RoleManager may change the Space's grants.
	RoleManager
	// RoleOwner is the Space's owner. Only personal Spaces have a user owner;
	// a project Space is owned by itself, so this never matches a caller there.
	RoleOwner
)

// String renders the role for logs and tests. It is not a wire format.
func (r Role) String() string {
	switch r {
	case RoleViewer:
		return "viewer"
	case RoleEditor:
		return "editor"
	case RoleManager:
		return "manager"
	case RoleOwner:
		return "owner"
	default:
		return "none"
	}
}

// Member is one principal's grant on a Space.
type Member struct {
	// Role is the authority derived from the grant's permission set.
	Role Role
	// ExpiresAt is when the grant lapses; the zero time means "never".
	ExpiresAt time.Time
	// Group reports whether the principal is a group rather than a user.
	Group bool
}

// Expired reports whether the grant has lapsed at now.
func (m Member) Expired(now time.Time) bool {
	return !m.ExpiresAt.IsZero() && !now.Before(m.ExpiresAt)
}

// RoleFor returns the caller's effective role on the Space: the highest of the
// Space ownership, the caller's own non-expired grant, and any non-expired grant
// held by a group the caller belongs to. RoleNone means "not a member" and must
// be answered exactly like "no such Space" so membership is not enumerable.
//
// groups are the caller's group ids; pass nil when they are unknown or when the
// Space has no group grants (see GroupGrants).
func (s Space) RoleFor(subject string, groups []string, now time.Time) Role {
	if subject == "" {
		return RoleNone
	}
	if s.Owner != "" && s.Owner == subject {
		return RoleOwner
	}
	best := RoleNone
	if m, ok := s.Members[subject]; ok && !m.Group && !m.Expired(now) && m.Role > best {
		best = m.Role
	}
	for _, g := range groups {
		if g == "" {
			continue
		}
		if m, ok := s.Members[g]; ok && m.Group && !m.Expired(now) && m.Role > best {
			best = m.Role
		}
	}
	return best
}

// GroupGrants reports whether the Space has any group grant. Callers use it to
// avoid resolving the caller's groups — an upstream call — for the common case
// of a Space granted to users only.
func (s Space) GroupGrants() bool {
	for _, m := range s.Members {
		if m.Group {
			return true
		}
	}
	return false
}

// parseMembers reads the Space's grants, group markers and grant expiries out of
// the Opaque map into principal -> Member. Personal Spaces carry no grants and
// yield nil. A grant whose permission set cannot be decoded is dropped rather
// than guessed at: an unreadable grant must not become authority.
func parseMembers(s *provider.StorageSpace) map[string]Member {
	op := s.GetOpaque()
	if op == nil {
		return nil
	}
	var raw map[string]json.RawMessage
	if !decodeOpaque(op.GetMap(), "grants", &raw) || len(raw) == 0 {
		return nil
	}

	var groups map[string]json.RawMessage
	decodeOpaque(op.GetMap(), "groups", &groups)
	var expirations map[string]*types.Timestamp
	decodeOpaque(op.GetMap(), "grants_expirations", &expirations)

	members := make(map[string]Member, len(raw))
	for principal, grant := range raw {
		var perms provider.ResourcePermissions
		if err := json.Unmarshal(grant, &perms); err != nil {
			continue
		}
		role := roleFromPermissions(&perms)
		if role == RoleNone {
			continue
		}
		_, isGroup := groups[principal]
		members[principal] = Member{
			Role:      role,
			ExpiresAt: timestampToTime(expirations[principal]),
			Group:     isGroup,
		}
	}
	if len(members) == 0 {
		return nil
	}
	return members
}

// decodeOpaque JSON-decodes one Opaque entry into v, reporting whether it was
// present and well-formed. An absent or malformed entry leaves v untouched;
// callers treat that as "no information", never as authority.
func decodeOpaque(m map[string]*types.OpaqueEntry, key string, v any) bool {
	entry, ok := m[key]
	if !ok || len(entry.GetValue()) == 0 {
		return false
	}
	return json.Unmarshal(entry.GetValue(), v) == nil
}

// roleFromPermissions maps a CS3 permission set onto the role ladder by
// capability. The ladder is checked top-down so the strongest capability wins.
func roleFromPermissions(p *provider.ResourcePermissions) Role {
	switch {
	case p.GetAddGrant() || p.GetUpdateGrant() || p.GetRemoveGrant() || p.GetDenyGrant():
		return RoleManager
	case p.GetInitiateFileUpload() || p.GetCreateContainer() || p.GetDelete() ||
		p.GetMove() || p.GetRestoreFileVersion() || p.GetRestoreRecycleItem() ||
		p.GetPurgeRecycle():
		return RoleEditor
	case p.GetStat() || p.GetGetPath() || p.GetListContainer() ||
		p.GetInitiateFileDownload() || p.GetListFileVersions() || p.GetListRecycle():
		return RoleViewer
	default:
		return RoleNone
	}
}

// timestampToTime converts a CS3 timestamp to UTC time; nil means "no expiry".
func timestampToTime(ts *types.Timestamp) time.Time {
	if ts == nil || (ts.GetSeconds() == 0 && ts.GetNanos() == 0) {
		return time.Time{}
	}
	return time.Unix(int64(ts.GetSeconds()), int64(ts.GetNanos())).UTC()
}
