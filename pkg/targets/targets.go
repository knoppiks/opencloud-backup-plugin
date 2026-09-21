// Package targets is the in-app, admin-managed backup-target store and its
// access model (decisions.md #12–#15).
//
// An OpenCloud admin creates S3 targets and grants each to all users or specific
// users/spaces. End users see and use only the targets granted to them.
//
// Hard rules from decisions.md #14 and AGENTS.md — enforced by this package's
// shape:
//   - Target S3 credentials are key-class. They are stored only as TW-wrapped
//     ciphertext (WrappedCreds), never persisted or returned in plaintext.
//   - Credentials are write-only across the API: no read path returns them.
//     Accordingly, the read type (Target) carries no plaintext credentials at
//     all; only the sealed blob is stored, and it is opened solely in worker
//     memory at run time via CredSealer.
//   - Never log credentials or the TW key.
//
// This file defines value types and boundary interfaces only. Storage
// (state.go), at-rest crypto (credsealer.go, reusing the key-envelope
// primitive), optional first-start seeding (bootstrap.go) and worker use all
// exist. The admin HTTP surface for creating targets and granting them does
// not — it lands with the UI in Phase 8, and until then the only writer is the
// seeder.
package targets

import (
	"context"
	"errors"
	"time"
)

// Target is an admin-managed S3 destination ("Buddy-S3"). It deliberately holds
// no plaintext credentials: the sealed credential blob lives in the store and is
// opened only at run time (decisions.md #14).
type Target struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Endpoint     string `json:"endpoint"`
	Region       string `json:"region,omitempty"`
	Bucket       string `json:"bucket"`
	Prefix       string `json:"prefix,omitempty"`
	UsePathStyle bool   `json:"use_path_style,omitempty"`
	DisableTLS   bool   `json:"disable_tls,omitempty"`
	// WrappedCreds is the TW-wrapped S3 credential blob (never plaintext, never
	// logged, never returned by any read API). Versioned like the key envelope.
	// It holds the target's whole CredentialSet — every Role — as one sealed
	// record, so a rotation re-seals one blob rather than keeping several in
	// step.
	WrappedCreds []byte    `json:"wrapped_creds,omitempty"`
	CreatedAt    time.Time `json:"created_at,omitzero"`
	UpdatedAt    time.Time `json:"updated_at,omitzero"`
	// Version is the WrappedCreds envelope version (compatibility promise).
	Version int `json:"version,omitempty"`
	// MaintenanceConfigured records whether WrappedCreds holds a separate
	// maintenance pair. It is metadata, not key material: a yes/no written at
	// seal time from what the writer supplied.
	//
	// It exists because the admin UI must be able to show whether a target is
	// credential-separated, and the only other way to answer that is to open
	// the sealed blob — which the admin path may not do (decisions.md #14).
	// Records written before this field read as false, which is the right
	// answer for the single-credential deployment they describe.
	MaintenanceConfigured bool `json:"maintenance_configured,omitempty"`
}

// PublicView is the least-disclosure projection returned to end users
// (decisions.md #12, phase-2 `GET /targets`): only what the UI needs to pick a
// target. No endpoint, bucket, or credentials.
type PublicView struct {
	ID   string
	Name string
}

// Public returns the end-user-safe projection of a Target.
func (t Target) Public() PublicView {
	return PublicView{ID: t.ID, Name: t.Name}
}

// GrantScope enumerates how a target is granted.
type GrantScope int

const (
	// ScopeUnknown is the zero value and must not be persisted.
	ScopeUnknown GrantScope = iota
	// ScopeAllUsers grants a target to every authenticated user.
	ScopeAllUsers
	// ScopeUser grants a target to a specific user (by OIDC sub).
	ScopeUser
	// ScopeSpace grants a target to members of a specific space.
	ScopeSpace
)

// Grant binds a target to an audience. Exactly one of the scope-specific fields
// is set, per Scope.
type Grant struct {
	TargetID string     `json:"target_id"`
	Scope    GrantScope `json:"scope"`
	// UserSub is set when Scope == ScopeUser.
	UserSub string `json:"user_sub,omitempty"`
	// SpaceID is set when Scope == ScopeSpace.
	SpaceID string `json:"space_id,omitempty"`
}

// errTargetIDRequired is returned by store writes that were handed no target.
var errTargetIDRequired = errors.New("targets: target id required")

// Validate reports whether a grant is well formed: a scope that was actually
// chosen, carrying exactly the field that scope defines.
//
// A grant is an authorization decision, so a malformed one is refused rather
// than interpreted. In particular the zero Scope is not "all users": it is a
// caller that never said who this target is for.
func (g Grant) Validate() error {
	switch g.Scope {
	case ScopeAllUsers:
		if g.UserSub != "" || g.SpaceID != "" {
			return errors.New("targets: an all-users grant names no user or space")
		}
	case ScopeUser:
		if g.UserSub == "" {
			return errors.New("targets: a user grant needs a user")
		}
		if g.SpaceID != "" {
			return errors.New("targets: a user grant names no space")
		}
	case ScopeSpace:
		if g.SpaceID == "" {
			return errors.New("targets: a space grant needs a space")
		}
		if g.UserSub != "" {
			return errors.New("targets: a space grant names no user")
		}
	default:
		return errors.New("targets: grant scope is required")
	}
	return nil
}

// PlainCreds are S3 credentials in plaintext. They exist only transiently:
// inbound on an admin create/update (write-only), or in worker memory after
// CredSealer.Open. Never persisted, never logged.
type PlainCreds struct {
	AccessKeyID     string
	SecretAccessKey string
}

// Complete reports whether both halves of the pair are present. A half-filled
// pair is a configuration mistake, never a way of saying "not configured".
func (c PlainCreds) Complete() bool {
	return c.AccessKeyID != "" && c.SecretAccessKey != ""
}

// Empty reports whether the pair is unset.
func (c PlainCreds) Empty() bool {
	return c.AccessKeyID == "" && c.SecretAccessKey == ""
}

// Role names what a run intends to do to a target, so the two can be given
// different credentials (decisions.md #9, Tier 2).
//
// The split is real in this service: a backup run never holds the maintenance
// credential and a maintenance run never holds the backup one. Whether it is
// also a *bound* depends entirely on the storage backend, and on the one this
// project ships against it is not — see the CredentialSet doc comment.
type Role string

const (
	// RoleBackup writes snapshots and reads them back. Backup and restore runs
	// use it.
	RoleBackup Role = "backup"
	// RoleMaintenance expires snapshots and reclaims their storage. Only prune
	// runs use it.
	RoleMaintenance Role = "maintenance"
)

// CredentialSet is a target's credentials, one pair per Role.
//
// Maintenance is optional: a target configured with a single credential leaves
// it empty and both roles resolve to Backup. That is the shape every target
// written before roles existed has, and it stays valid — a deployment with one
// key is not misconfigured, it is just not separated.
//
// # What separating them does and does not buy
//
// On a backend that can express "may write, may not delete" the separation is a
// bound: a leaked backup credential cannot destroy history. Garage — the target
// this project ships against — cannot express it. Its grants are read, write and
// owner; write includes DeleteObject, owner covers administering the bucket and
// grants no object access at all, and a key without read cannot open an
// encrypted repository. Both roles therefore end up holding read+write, which is
// the same capability. TestGarageGrantMatrix pins this rather than asserting it
// from documentation.
//
// So on Garage the separation is organisational: two keys that can be rotated
// and revoked independently, and an audit trail that distinguishes the actor
// that wrote a backup from the actor that deleted one. It is not a blast-radius
// bound, and the operations runbook says so in those words.
type CredentialSet struct {
	Backup      PlainCreds
	Maintenance PlainCreds
}

// For returns the credentials a run in the given role must use. An unconfigured
// maintenance pair falls back to the backup pair, which is the single-credential
// deployment.
func (c CredentialSet) For(role Role) PlainCreds {
	if role == RoleMaintenance && c.Maintenance.Complete() {
		return c.Maintenance
	}
	return c.Backup
}

// Separated reports whether the roles actually resolve to different keys. Used
// for diagnostics; it never decides anything.
func (c CredentialSet) Separated() bool {
	return c.Maintenance.Complete() && c.Maintenance.AccessKeyID != c.Backup.AccessKeyID
}

// Store persists targets and grants. It stores credentials only as the sealed
// blob on Target.WrappedCreds and never exposes plaintext.
type Store interface {
	// CreateTarget persists a target (with its already-sealed WrappedCreds).
	CreateTarget(ctx context.Context, t Target) (Target, error)
	// UpdateTarget updates target metadata. If WrappedCreds is nil the stored
	// credentials are left unchanged (supports "edit without re-entering creds",
	// phase-8).
	UpdateTarget(ctx context.Context, t Target) (Target, error)
	// DeleteTarget removes a target and its grants.
	DeleteTarget(ctx context.Context, id string) error
	// GetTarget returns a target by ID (including WrappedCreds for worker use).
	GetTarget(ctx context.Context, id string) (Target, error)
	// ListTargets returns all targets (admin view).
	ListTargets(ctx context.Context) ([]Target, error)

	// PutGrant adds or replaces a grant.
	PutGrant(ctx context.Context, g Grant) error
	// DeleteGrant removes a grant.
	DeleteGrant(ctx context.Context, g Grant) error
	// ListGrants returns the grants for a target.
	ListGrants(ctx context.Context, targetID string) ([]Grant, error)
	// ReplaceGrants sets a target's whole audience in one write. The admin API
	// edits grants as a set, and the durable store keeps them as one document
	// per version, so applying such an edit as a sequence of PutGrant and
	// DeleteGrant calls would leave a crash able to stop halfway through — with
	// a grant list nobody chose. Passing an empty list revokes everything,
	// which is a decision an admin can legitimately make and is therefore not
	// the same as deleting the target.
	ReplaceGrants(ctx context.Context, targetID string, grants []Grant) error
}

// CredSealer seals and opens S3 credentials using the cluster/KMS Target-Wrap
// (TW) key (decisions.md #14). Seal happens on admin write; Open happens only in
// worker memory at run time. The implementation reuses the maintained
// AEAD-envelope primitive from pkg/keys — no hand-rolled crypto.
type CredSealer interface {
	// Seal wraps a target's credentials for storage.
	Seal(c CredentialSet) (wrapped []byte, version int, err error)
	// Open unwraps stored credentials for immediate, in-memory use.
	Open(wrapped []byte) (CredentialSet, error)
}

// Authorizer answers what a given user may see and use. All grant checks are
// server-side; client-supplied target IDs are never trusted (decisions.md #12,
// phase-2 authorization rule).
type Authorizer interface {
	// VisibleTargets returns the targets granted to the user, given the spaces
	// they are a member of. Returns least-disclosure projections.
	VisibleTargets(ctx context.Context, userSub string, spaceIDs []string) ([]PublicView, error)
	// MayUse reports whether the user may use target targetID for space spaceID.
	MayUse(ctx context.Context, userSub, spaceID, targetID string) (bool, error)
}

// ErrNotFound is returned when a target does not exist.
type ErrNotFound struct{ ID string }

func (e ErrNotFound) Error() string { return "targets: no target with id " + e.ID }
