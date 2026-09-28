package targets

// Grant validation and whole-list replacement — the write path the admin API
// uses (phase-8-web-ui.md, sub-phase 8b).
//
// A grant is an authorization decision, so the tests below are about refusals
// as much as about writes: a malformed grant must not be stored in some
// interpreted form, and a refused list must leave the previous audience exactly
// as it was.

import (
	"context"
	"testing"

	"opencloud-backup-plugin/pkg/state"
)

func TestGrantValidate(t *testing.T) {
	cases := []struct {
		name  string
		grant Grant
		valid bool
	}{
		{"all users", Grant{TargetID: "t", Scope: ScopeAllUsers}, true},
		{"user", Grant{TargetID: "t", Scope: ScopeUser, UserSub: "alice"}, true},
		{"space", Grant{TargetID: "t", Scope: ScopeSpace, SpaceID: "s1"}, true},

		// The zero scope is the one that matters: it is what a client sends by
		// forgetting a field, and reading it as "everyone" would turn an
		// omission into a grant.
		{"no scope", Grant{TargetID: "t"}, false},
		{"unknown scope", Grant{TargetID: "t", Scope: GrantScope(99)}, false},

		{"user grant without a user", Grant{TargetID: "t", Scope: ScopeUser}, false},
		{"space grant without a space", Grant{TargetID: "t", Scope: ScopeSpace}, false},
		{"all-users grant naming a user",
			Grant{TargetID: "t", Scope: ScopeAllUsers, UserSub: "alice"}, false},
		{"user grant naming a space",
			Grant{TargetID: "t", Scope: ScopeUser, UserSub: "alice", SpaceID: "s1"}, false},
		{"space grant naming a user",
			Grant{TargetID: "t", Scope: ScopeSpace, SpaceID: "s1", UserSub: "alice"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.grant.Validate()
			if tc.valid && err != nil {
				t.Fatalf("valid grant refused: %v", err)
			}
			if !tc.valid && err == nil {
				t.Fatal("invalid grant accepted")
			}
		})
	}
}

// Both implementations must agree on ReplaceGrants down to the refusals: it is
// an authorization boundary, and a divergence between the test double and the
// deployed store would be a security bug rather than a cosmetic one.
func TestStoreContract_ReplaceGrants(t *testing.T) {
	implementations := map[string]func() Store{
		"memory": func() Store { return NewMemoryStore() },
		"state":  func() Store { return NewStateStore(state.NewMemoryStore()) },
	}

	for name, newImpl := range implementations {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := newImpl()
			if _, err := store.CreateTarget(ctx, Target{ID: "t1", Name: "buddy"}); err != nil {
				t.Fatalf("CreateTarget: %v", err)
			}
			auth, ok := store.(Authorizer)
			if !ok {
				t.Fatal("store must also be an Authorizer")
			}

			// A whole audience in one write.
			if err := store.ReplaceGrants(ctx, "t1", []Grant{
				{Scope: ScopeUser, UserSub: "alice"},
				{Scope: ScopeSpace, SpaceID: "s1"},
			}); err != nil {
				t.Fatalf("ReplaceGrants: %v", err)
			}
			if got := listGrants(t, store, "t1"); len(got) != 2 {
				t.Fatalf("grants = %+v, want 2", got)
			}
			if allowed, err := auth.MayUse(ctx, "alice", "", "t1"); err != nil || !allowed {
				t.Fatalf("MayUse(alice) = %v, %v", allowed, err)
			}
			if allowed, err := auth.MayUse(ctx, "bob", "s1", "t1"); err != nil || !allowed {
				t.Fatalf("MayUse(bob via space) = %v, %v", allowed, err)
			}

			// Replace, not merge: the previous audience is gone.
			if err := store.ReplaceGrants(ctx, "t1", []Grant{
				{Scope: ScopeUser, UserSub: "bob"},
			}); err != nil {
				t.Fatalf("ReplaceGrants second: %v", err)
			}
			if allowed, err := auth.MayUse(ctx, "alice", "s1", "t1"); err != nil || allowed {
				t.Fatalf("a replaced-away grant still admits alice: %v, %v", allowed, err)
			}

			// The body cannot retarget a grant at another target: the id comes
			// from the path, so a caller cannot grant themselves somebody
			// else's target through this route.
			if err := store.ReplaceGrants(ctx, "t1", []Grant{
				{TargetID: "t-elsewhere", Scope: ScopeUser, UserSub: "carol"},
			}); err != nil {
				t.Fatalf("ReplaceGrants third: %v", err)
			}
			for _, g := range listGrants(t, store, "t1") {
				if g.TargetID != "t1" {
					t.Fatalf("a grant was stored against %q", g.TargetID)
				}
			}
			if got := listGrants(t, store, "t-elsewhere"); len(got) != 0 {
				t.Fatalf("grants leaked onto another target: %+v", got)
			}

			// Exact duplicates collapse.
			if err := store.ReplaceGrants(ctx, "t1", []Grant{
				{Scope: ScopeAllUsers},
				{Scope: ScopeAllUsers},
			}); err != nil {
				t.Fatalf("ReplaceGrants duplicates: %v", err)
			}
			if got := listGrants(t, store, "t1"); len(got) != 1 {
				t.Fatalf("duplicate grants stored: %+v", got)
			}

			// An invalid grant anywhere in the list refuses the whole write,
			// and leaves the stored audience untouched. Applying the valid
			// prefix would give an admin an audience they never asked for.
			err := store.ReplaceGrants(ctx, "t1", []Grant{
				{Scope: ScopeUser, UserSub: "dave"},
				{Scope: ScopeUser}, // no user
			})
			if err == nil {
				t.Fatal("a malformed grant was accepted")
			}
			got := listGrants(t, store, "t1")
			if len(got) != 1 || got[0].Scope != ScopeAllUsers {
				t.Fatalf("a refused write changed the stored audience: %+v", got)
			}

			// An empty list revokes everything — a legitimate instruction, and
			// not the same as deleting the target.
			if err := store.ReplaceGrants(ctx, "t1", nil); err != nil {
				t.Fatalf("ReplaceGrants empty: %v", err)
			}
			if got := listGrants(t, store, "t1"); len(got) != 0 {
				t.Fatalf("grants survived a revoke-all: %+v", got)
			}
			if _, err := store.GetTarget(ctx, "t1"); err != nil {
				t.Fatalf("revoking every grant deleted the target: %v", err)
			}

			if err := store.ReplaceGrants(ctx, "", nil); err == nil {
				t.Fatal("ReplaceGrants with no target id was accepted")
			}
		})
	}
}

// PutGrant guards the same boundary from the other direction: the seeder and
// any future single-grant caller must not be able to store a grant that means
// nothing.
func TestStoreContract_PutGrantRefusesAMalformedGrant(t *testing.T) {
	implementations := map[string]func() Store{
		"memory": func() Store { return NewMemoryStore() },
		"state":  func() Store { return NewStateStore(state.NewMemoryStore()) },
	}

	for name, newImpl := range implementations {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := newImpl()
			if _, err := store.CreateTarget(ctx, Target{ID: "t1"}); err != nil {
				t.Fatalf("CreateTarget: %v", err)
			}
			if err := store.PutGrant(ctx, Grant{TargetID: "t1"}); err == nil {
				t.Fatal("a grant with no scope was stored")
			}
			if got := listGrants(t, store, "t1"); len(got) != 0 {
				t.Fatalf("grants = %+v, want none", got)
			}
		})
	}
}

func listGrants(t *testing.T, store Store, targetID string) []Grant {
	t.Helper()
	got, err := store.ListGrants(context.Background(), targetID)
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	return got
}
