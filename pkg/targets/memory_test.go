package targets

import (
	"context"
	"testing"
)

func seedTarget(t *testing.T, m *MemoryStore, id, name string) {
	t.Helper()
	if _, err := m.CreateTarget(context.Background(), Target{ID: id, Name: name, WrappedCreds: []byte("sealed")}); err != nil {
		t.Fatalf("CreateTarget(%s): %v", id, err)
	}
}

func viewIDs(vs []PublicView) map[string]bool {
	out := make(map[string]bool, len(vs))
	for _, v := range vs {
		out[v.ID] = true
	}
	return out
}

func TestVisibleTargets_AllUsersGrant(t *testing.T) {
	m := NewMemoryStore()
	seedTarget(t, m, "t1", "Buddy")
	if err := m.PutGrant(context.Background(), Grant{TargetID: "t1", Scope: ScopeAllUsers}); err != nil {
		t.Fatal(err)
	}
	vs, err := m.VisibleTargets(context.Background(), "anyone", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !viewIDs(vs)["t1"] {
		t.Fatalf("all-users grant should be visible to anyone: %+v", vs)
	}
}

func TestVisibleTargets_UserGrantScoped(t *testing.T) {
	m := NewMemoryStore()
	seedTarget(t, m, "t1", "Buddy")
	seedTarget(t, m, "t2", "Other")
	if err := m.PutGrant(context.Background(), Grant{TargetID: "t1", Scope: ScopeUser, UserSub: "alice"}); err != nil {
		t.Fatal(err)
	}

	alice, _ := m.VisibleTargets(context.Background(), "alice", nil)
	if !viewIDs(alice)["t1"] || viewIDs(alice)["t2"] {
		t.Fatalf("alice should see only t1: %+v", alice)
	}
	bob, _ := m.VisibleTargets(context.Background(), "bob", nil)
	if len(bob) != 0 {
		t.Fatalf("bob should see no targets: %+v", bob)
	}
}

func TestVisibleTargets_SpaceGrant(t *testing.T) {
	m := NewMemoryStore()
	seedTarget(t, m, "t1", "Buddy")
	if err := m.PutGrant(context.Background(), Grant{TargetID: "t1", Scope: ScopeSpace, SpaceID: "spaceA"}); err != nil {
		t.Fatal(err)
	}

	member, _ := m.VisibleTargets(context.Background(), "alice", []string{"spaceA"})
	if !viewIDs(member)["t1"] {
		t.Fatalf("member of spaceA should see t1: %+v", member)
	}
	outsider, _ := m.VisibleTargets(context.Background(), "alice", []string{"spaceB"})
	if len(outsider) != 0 {
		t.Fatalf("non-member should see nothing: %+v", outsider)
	}
}

func TestVisibleTargets_ReturnsOnlyPublicFields(t *testing.T) {
	m := NewMemoryStore()
	if _, err := m.CreateTarget(context.Background(), Target{
		ID: "t1", Name: "Buddy", Endpoint: "http://secret:3900", Bucket: "b",
		WrappedCreds: []byte("sealed"),
	}); err != nil {
		t.Fatal(err)
	}
	_ = m.PutGrant(context.Background(), Grant{TargetID: "t1", Scope: ScopeAllUsers})

	vs, _ := m.VisibleTargets(context.Background(), "u", nil)
	if len(vs) != 1 || vs[0].ID != "t1" || vs[0].Name != "Buddy" {
		t.Fatalf("unexpected public view: %+v", vs)
	}
	// PublicView is {ID,Name} by type — the compiler guarantees no creds/endpoint
	// leak. This test documents the intent and guards behaviour.
}

func TestMayUse_ServerSide(t *testing.T) {
	m := NewMemoryStore()
	seedTarget(t, m, "t1", "Buddy")
	_ = m.PutGrant(context.Background(), Grant{TargetID: "t1", Scope: ScopeUser, UserSub: "alice"})

	ok, err := m.MayUse(context.Background(), "alice", "", "t1")
	if err != nil || !ok {
		t.Fatalf("alice may use t1: ok=%v err=%v", ok, err)
	}
	ok, _ = m.MayUse(context.Background(), "bob", "", "t1")
	if ok {
		t.Fatal("bob must not use t1")
	}
	_, err = m.MayUse(context.Background(), "alice", "", "missing")
	if err == nil {
		t.Fatal("MayUse on unknown target should error (not silently allow)")
	}
}

func TestGrantLifecycle(t *testing.T) {
	m := NewMemoryStore()
	seedTarget(t, m, "t1", "Buddy")
	g := Grant{TargetID: "t1", Scope: ScopeUser, UserSub: "alice"}
	_ = m.PutGrant(context.Background(), g)
	_ = m.PutGrant(context.Background(), g) // idempotent replace

	list, _ := m.ListGrants(context.Background(), "t1")
	if len(list) != 1 {
		t.Fatalf("expected 1 grant after idempotent put, got %d", len(list))
	}
	_ = m.DeleteGrant(context.Background(), g)
	list, _ = m.ListGrants(context.Background(), "t1")
	if len(list) != 0 {
		t.Fatalf("expected 0 grants after delete, got %d", len(list))
	}
}

func TestUpdateTargetPreservesCredsWhenNil(t *testing.T) {
	m := NewMemoryStore()
	if _, err := m.CreateTarget(context.Background(), Target{ID: "t1", Name: "Buddy", WrappedCreds: []byte("sealed"), Version: 3}); err != nil {
		t.Fatal(err)
	}
	updated, err := m.UpdateTarget(context.Background(), Target{ID: "t1", Name: "Renamed"})
	if err != nil {
		t.Fatal(err)
	}
	if string(updated.WrappedCreds) != "sealed" || updated.Version != 3 {
		t.Fatalf("nil WrappedCreds must preserve stored creds/version: %+v", updated)
	}
	if updated.Name != "Renamed" {
		t.Fatalf("metadata not updated: %+v", updated)
	}
}

func TestDeleteTargetRemovesGrants(t *testing.T) {
	m := NewMemoryStore()
	seedTarget(t, m, "t1", "Buddy")
	_ = m.PutGrant(context.Background(), Grant{TargetID: "t1", Scope: ScopeAllUsers})
	if err := m.DeleteTarget(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if list, _ := m.ListGrants(context.Background(), "t1"); len(list) != 0 {
		t.Fatalf("grants should be gone with target: %+v", list)
	}
	if err := m.DeleteTarget(context.Background(), "t1"); err == nil {
		t.Fatal("deleting missing target should error")
	}
}
