package targets

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"opencloud-backup-plugin/pkg/state"
)

// Both Store implementations must behave identically, including their grant
// evaluation: this is an authorization boundary (decisions.md #12), so a
// divergence between the tested double and the deployed store would be a
// security bug, not a cosmetic one.
func TestStoreContract(t *testing.T) {
	implementations := map[string]func() Store{
		"memory": func() Store { return NewMemoryStore() },
		"state":  func() Store { return NewStateStore(state.NewMemoryStore()) },
	}

	for name, newImpl := range implementations {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := newImpl()

			var nf ErrNotFound
			if _, err := store.GetTarget(ctx, "absent"); !errors.As(err, &nf) {
				t.Fatalf("GetTarget absent: %v", err)
			}
			if err := store.DeleteTarget(ctx, "absent"); !errors.As(err, &nf) {
				t.Fatalf("DeleteTarget absent: %v", err)
			}
			if _, err := store.UpdateTarget(ctx, Target{ID: "absent"}); !errors.As(err, &nf) {
				t.Fatalf("UpdateTarget absent: %v", err)
			}

			created, err := store.CreateTarget(ctx, Target{
				ID:           "t1",
				Name:         "buddy",
				Endpoint:     "garage.internal:3900",
				Bucket:       "backups",
				WrappedCreds: []byte("sealed"),
				Version:      1,
			})
			if err != nil {
				t.Fatalf("CreateTarget: %v", err)
			}
			if created.ID != "t1" {
				t.Fatalf("created = %+v", created)
			}

			// A metadata edit with no credentials must keep the sealed blob.
			updated, err := store.UpdateTarget(ctx, Target{ID: "t1", Name: "renamed", Bucket: "backups"})
			if err != nil {
				t.Fatalf("UpdateTarget: %v", err)
			}
			if !bytes.Equal(updated.WrappedCreds, []byte("sealed")) || updated.Version != 1 {
				t.Fatalf("credentials were lost on a metadata edit: %+v", updated)
			}
			if updated.Name != "renamed" {
				t.Fatalf("updated = %+v", updated)
			}

			list, err := store.ListTargets(ctx)
			if err != nil || len(list) != 1 {
				t.Fatalf("ListTargets = %+v (%v)", list, err)
			}

			grant := Grant{TargetID: "t1", Scope: ScopeUser, UserSub: "alice"}
			if err := store.PutGrant(ctx, grant); err != nil {
				t.Fatalf("PutGrant: %v", err)
			}
			// Idempotent: putting the same grant twice must not duplicate it.
			if err := store.PutGrant(ctx, grant); err != nil {
				t.Fatalf("PutGrant twice: %v", err)
			}
			grants, err := store.ListGrants(ctx, "t1")
			if err != nil || len(grants) != 1 {
				t.Fatalf("ListGrants = %+v (%v)", grants, err)
			}

			auth, ok := store.(Authorizer)
			if !ok {
				t.Fatal("store must also be an Authorizer")
			}
			if allowed, err := auth.MayUse(ctx, "alice", "s1", "t1"); err != nil || !allowed {
				t.Fatalf("MayUse(alice) = %v, %v", allowed, err)
			}
			if allowed, err := auth.MayUse(ctx, "mallory", "s1", "t1"); err != nil || allowed {
				t.Fatalf("MayUse(mallory) = %v, %v", allowed, err)
			}
			views, err := auth.VisibleTargets(ctx, "alice", []string{"s1"})
			if err != nil || len(views) != 1 || views[0].ID != "t1" {
				t.Fatalf("VisibleTargets(alice) = %+v (%v)", views, err)
			}
			if views, err := auth.VisibleTargets(ctx, "mallory", nil); err != nil || len(views) != 0 {
				t.Fatalf("VisibleTargets(mallory) = %+v (%v)", views, err)
			}

			if err := store.DeleteGrant(ctx, grant); err != nil {
				t.Fatalf("DeleteGrant: %v", err)
			}
			if allowed, err := auth.MayUse(ctx, "alice", "s1", "t1"); err != nil || allowed {
				t.Fatalf("MayUse after revoke = %v, %v", allowed, err)
			}

			if err := store.DeleteTarget(ctx, "t1"); err != nil {
				t.Fatalf("DeleteTarget: %v", err)
			}
			if _, err := store.GetTarget(ctx, "t1"); !errors.As(err, &nf) {
				t.Fatalf("GetTarget after delete: %v", err)
			}
		})
	}
}

// A target whose id is reused must not inherit the previous target's grants.
func TestStateStore_DeleteTargetDropsItsGrants(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	store := NewStateStore(backing)

	if _, err := store.CreateTarget(ctx, Target{ID: "t1", Name: "buddy"}); err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	if err := store.PutGrant(ctx, Grant{TargetID: "t1", Scope: ScopeAllUsers}); err != nil {
		t.Fatalf("PutGrant: %v", err)
	}
	if err := store.DeleteTarget(ctx, "t1"); err != nil {
		t.Fatalf("DeleteTarget: %v", err)
	}

	if _, err := store.CreateTarget(ctx, Target{ID: "t1", Name: "reused"}); err != nil {
		t.Fatalf("CreateTarget again: %v", err)
	}
	grants, err := store.ListGrants(ctx, "t1")
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("grants outlived their target: %+v", grants)
	}
}

func TestStateStore_SurvivesRestart(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()

	before := NewStateStore(backing)
	if _, err := before.CreateTarget(ctx, Target{
		ID:           "t1",
		Name:         "buddy",
		Bucket:       "backups",
		WrappedCreds: []byte("sealed"),
		Version:      1,
	}); err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	if err := before.PutGrant(ctx, Grant{TargetID: "t1", Scope: ScopeAllUsers}); err != nil {
		t.Fatalf("PutGrant: %v", err)
	}

	after := NewStateStore(backing)
	got, err := after.GetTarget(ctx, "t1")
	if err != nil {
		t.Fatalf("GetTarget after restart: %v", err)
	}
	if !bytes.Equal(got.WrappedCreds, []byte("sealed")) || got.Name != "buddy" {
		t.Fatalf("target after restart: %+v", got)
	}
	if allowed, err := after.MayUse(ctx, "anyone", "s1", "t1"); err != nil || !allowed {
		t.Fatalf("MayUse after restart = %v, %v", allowed, err)
	}
}
