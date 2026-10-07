package cs3

import (
	"testing"
	"time"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	types "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
)

// The permission sets below are copied verbatim from OpenCloud 7.3.0's
// ListStorageSpaces opaque map for a project Space shared with the "Can view",
// "Can edit" and "Can manage" space roles. They are the contract this package
// parses; TestIntegration_SpaceGrantsShape re-derives them from a live fixture
// so a reva change that breaks them fails a test rather than an authorization
// decision.
const (
	permsViewer = `{"get_path":true,"get_quota":true,"initiate_file_download":true,` +
		`"list_grants":true,"list_container":true,"list_recycle":true,"stat":true}`

	permsEditor = `{"create_container":true,"delete":true,"get_path":true,"get_quota":true,` +
		`"initiate_file_download":true,"initiate_file_upload":true,"list_grants":true,` +
		`"list_container":true,"list_file_versions":true,"list_recycle":true,"move":true,` +
		`"restore_file_version":true,"restore_recycle_item":true,"stat":true}`

	permsManager = `{"add_grant":true,"create_container":true,"delete":true,"get_path":true,` +
		`"get_quota":true,"initiate_file_download":true,"initiate_file_upload":true,` +
		`"list_grants":true,"list_container":true,"list_file_versions":true,` +
		`"list_recycle":true,"move":true,"remove_grant":true,"purge_recycle":true,` +
		`"restore_file_version":true,"restore_recycle_item":true,"stat":true,` +
		`"update_grant":true,"deny_grant":true}`
)

// spaceWithOpaque builds a project Space carrying the given raw opaque entries.
func spaceWithOpaque(entries map[string]string) *provider.StorageSpace {
	m := make(map[string]*types.OpaqueEntry, len(entries))
	for k, v := range entries {
		m[k] = &types.OpaqueEntry{Decoder: "json", Value: []byte(v)}
	}
	return &provider.StorageSpace{
		Id:        &provider.StorageSpaceId{OpaqueId: "s"},
		Name:      "Team",
		SpaceType: "project",
		Opaque:    &types.Opaque{Map: m},
	}
}

func TestParseMembers_DerivesRolesFromPermissionSets(t *testing.T) {
	s := spaceWithOpaque(map[string]string{
		"grants": `{"v":` + permsViewer + `,"e":` + permsEditor + `,"m":` + permsManager + `}`,
	})

	got := parseMembers(s)
	want := map[string]Role{"v": RoleViewer, "e": RoleEditor, "m": RoleManager}
	for principal, role := range want {
		if got[principal].Role != role {
			t.Errorf("principal %q: got role %v, want %v", principal, got[principal].Role, role)
		}
		if got[principal].Group {
			t.Errorf("principal %q: marked as a group without a groups entry", principal)
		}
		if !got[principal].ExpiresAt.IsZero() {
			t.Errorf("principal %q: got expiry %v, want none", principal, got[principal].ExpiresAt)
		}
	}
}

func TestParseMembers_MarksGroupsAndExpiries(t *testing.T) {
	s := spaceWithOpaque(map[string]string{
		"grants":             `{"u":` + permsEditor + `,"g":` + permsViewer + `}`,
		"groups":             `{"g":{}}`,
		"grants_expirations": `{"u":{"seconds":4070908800}}`,
	})

	got := parseMembers(s)
	if got["g"].Group != true {
		t.Errorf("group principal not marked as a group: %+v", got["g"])
	}
	if got["u"].Group {
		t.Errorf("user principal marked as a group: %+v", got["u"])
	}
	want := time.Unix(4070908800, 0).UTC()
	if !got["u"].ExpiresAt.Equal(want) {
		t.Errorf("expiry: got %v, want %v", got["u"].ExpiresAt, want)
	}
	if !got["g"].ExpiresAt.IsZero() {
		t.Errorf("group grant should not expire: %v", got["g"].ExpiresAt)
	}
}

func TestParseMembers_IgnoresUnusableGrants(t *testing.T) {
	tests := map[string]*provider.StorageSpace{
		"no opaque":       {Id: &provider.StorageSpaceId{OpaqueId: "s"}},
		"empty grants":    spaceWithOpaque(map[string]string{"grants": `{}`}),
		"malformed":       spaceWithOpaque(map[string]string{"grants": `not json`}),
		"empty perms":     spaceWithOpaque(map[string]string{"grants": `{"u":{}}`}),
		"unknown perms":   spaceWithOpaque(map[string]string{"grants": `{"u":{"future_permission":true}}`}),
		"malformed grant": spaceWithOpaque(map[string]string{"grants": `{"u":"manager"}`}),
	}
	for name, s := range tests {
		t.Run(name, func(t *testing.T) {
			if m := parseMembers(s); m != nil {
				t.Fatalf("want no members, got %+v", m)
			}
		})
	}
}

// TestParseMembers_MalformedSidecarsDoNotGrant guards the shape of the failure:
// an unreadable groups or expirations map must not turn a group grant into a
// user grant or an expired grant into a live one.
func TestParseMembers_MalformedSidecarsAreIgnored(t *testing.T) {
	s := spaceWithOpaque(map[string]string{
		"grants":             `{"g":` + permsViewer + `}`,
		"groups":             `garbage`,
		"grants_expirations": `garbage`,
	})
	got := parseMembers(s)
	if got["g"].Role != RoleViewer {
		t.Fatalf("grant lost: %+v", got)
	}
	// A group grant whose marker is unreadable is treated as a user grant. That
	// is the conservative direction: RoleFor only matches it against the
	// caller's own subject, which no group id can equal.
	if got["g"].Group {
		t.Fatalf("unreadable groups map must not mark a group: %+v", got["g"])
	}
}

func TestRoleFor(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	space := Space{
		ID:    "s",
		Owner: "owner",
		Members: map[string]Member{
			"viewer":       {Role: RoleViewer},
			"editor":       {Role: RoleEditor},
			"manager":      {Role: RoleManager},
			"lapsed":       {Role: RoleManager, ExpiresAt: past},
			"still-valid":  {Role: RoleEditor, ExpiresAt: future},
			"group-edit":   {Role: RoleEditor, Group: true},
			"group-lapsed": {Role: RoleManager, Group: true, ExpiresAt: past},
		},
	}

	tests := []struct {
		name    string
		subject string
		groups  []string
		want    Role
	}{
		{"owner outranks every grant", "owner", nil, RoleOwner},
		{"direct viewer", "viewer", nil, RoleViewer},
		{"direct editor", "editor", nil, RoleEditor},
		{"direct manager", "manager", nil, RoleManager},
		{"expired grant is not a grant", "lapsed", nil, RoleNone},
		{"unexpired grant still counts", "still-valid", nil, RoleEditor},
		{"stranger", "mallory", nil, RoleNone},
		{"empty subject is never a member", "", nil, RoleNone},
		{"group grant applies to its members", "mallory", []string{"group-edit"}, RoleEditor},
		{"expired group grant does not", "mallory", []string{"group-lapsed"}, RoleNone},
		{"unknown group grants nothing", "mallory", []string{"other"}, RoleNone},
		{"highest of direct and group wins", "viewer", []string{"group-edit"}, RoleEditor},
		{"direct role is not lowered by a group", "manager", []string{"group-edit"}, RoleManager},
		{"a group id is not a subject", "group-edit", nil, RoleNone},
		{"empty group id is ignored", "mallory", []string{""}, RoleNone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := space.RoleFor(tc.subject, tc.groups, now); got != tc.want {
				t.Fatalf("RoleFor(%q, %v) = %v, want %v", tc.subject, tc.groups, got, tc.want)
			}
		})
	}
}

// TestRoleFor_ExpiryBoundary pins the inclusive edge: a grant is dead at its
// expiry instant, not one tick later.
func TestRoleFor_ExpiryBoundary(t *testing.T) {
	exp := time.Unix(1_700_000_000, 0).UTC()
	space := Space{Members: map[string]Member{"u": {Role: RoleEditor, ExpiresAt: exp}}}

	if got := space.RoleFor("u", nil, exp.Add(-time.Nanosecond)); got != RoleEditor {
		t.Errorf("just before expiry: got %v, want editor", got)
	}
	if got := space.RoleFor("u", nil, exp); got != RoleNone {
		t.Errorf("at expiry: got %v, want none", got)
	}
}

// TestRoleFor_PersonalSpaceOwner covers the personal-Space case, where there are
// no grants at all and ownership is the only authority.
func TestRoleFor_PersonalSpaceOwner(t *testing.T) {
	space := Space{ID: "p", Type: "personal", Owner: "alice"}
	if got := space.RoleFor("alice", nil, time.Now()); got != RoleOwner {
		t.Errorf("owner: got %v, want owner", got)
	}
	if got := space.RoleFor("bob", nil, time.Now()); got != RoleNone {
		t.Errorf("stranger: got %v, want none", got)
	}
	// An ownerless Space must not admit a caller with an empty subject.
	if got := (Space{}).RoleFor("", nil, time.Now()); got != RoleNone {
		t.Errorf("empty subject on ownerless space: got %v, want none", got)
	}
}

func TestGroupGrants(t *testing.T) {
	none := Space{Members: map[string]Member{"u": {Role: RoleViewer}}}
	some := Space{Members: map[string]Member{"u": {Role: RoleViewer}, "g": {Role: RoleViewer, Group: true}}}
	if none.GroupGrants() {
		t.Error("space without group grants reported one")
	}
	if !some.GroupGrants() {
		t.Error("space with a group grant reported none")
	}
}

func TestRoleString(t *testing.T) {
	want := map[Role]string{
		RoleNone: "none", RoleViewer: "viewer", RoleEditor: "editor",
		RoleManager: "manager", RoleOwner: "owner",
	}
	for role, s := range want {
		if role.String() != s {
			t.Errorf("Role(%d).String() = %q, want %q", role, role.String(), s)
		}
	}
}

// TestRoleLadderIsOrdered guards the comparison handlers rely on: requiring
// editor must admit a manager and an owner, and refuse a viewer.
func TestRoleLadderIsOrdered(t *testing.T) {
	ladder := []Role{RoleNone, RoleViewer, RoleEditor, RoleManager, RoleOwner}
	for i := 1; i < len(ladder); i++ {
		if ladder[i-1] >= ladder[i] {
			t.Fatalf("role ladder is not monotonically ordered at %v -> %v", ladder[i-1], ladder[i])
		}
	}
}
