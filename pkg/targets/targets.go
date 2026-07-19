// Package targets is the in-app, admin-managed backup-target store and its
// access model (decisions.md #12–#15).
//
// An OpenCloud admin creates S3 targets and grants each to all users or specific
// users/spaces. End users see and use only the targets granted to them.
//
// Hard rules from decisions.md #14 and AGENTS.md — enforced by this package's
// shape, implemented in a later phase:
//   - Target S3 credentials are key-class. They are stored only as TW-wrapped
//     ciphertext (WrappedCreds), never persisted or returned in plaintext.
//   - Credentials are write-only across the API: no read path returns them.
//     Accordingly, the read type (Target) carries no plaintext credentials at
//     all; only the sealed blob is stored, and it is opened solely in worker
//     memory at run time via CredSealer.
//   - Never log credentials or the TW key.
//
// This file defines value types and boundary interfaces only; storage, crypto,
// and enforcement are implemented in their respective phases (target store +
// admin API in Phase 2/adjacent; at-rest crypto reuses the Phase 3 envelope
// primitive; worker use in Phase 4).
package targets

import (
	"context"
	"time"
)

// Target is an admin-managed S3 destination ("Buddy-S3"). It deliberately holds
// no plaintext credentials: the sealed credential blob lives in the store and is
// opened only at run time (decisions.md #14).
type Target struct {
	ID           string
	Name         string
	Endpoint     string
	Region       string
	Bucket       string
	Prefix       string
	UsePathStyle bool
	DisableTLS   bool
	// WrappedCreds is the TW-wrapped S3 credential blob (never plaintext, never
	// logged, never returned by any read API). Versioned like the key envelope.
	WrappedCreds []byte
	CreatedAt    time.Time
	UpdatedAt    time.Time
	// Version is the WrappedCreds envelope version (compatibility promise).
	Version int
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
	TargetID string
	Scope    GrantScope
	// UserSub is set when Scope == ScopeUser.
	UserSub string
	// SpaceID is set when Scope == ScopeSpace.
	SpaceID string
}

// PlainCreds are S3 credentials in plaintext. They exist only transiently:
// inbound on an admin create/update (write-only), or in worker memory after
// CredSealer.Open. Never persisted, never logged.
type PlainCreds struct {
	AccessKeyID     string
	SecretAccessKey string
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
}

// CredSealer seals and opens S3 credentials using the cluster/KMS Target-Wrap
// (TW) key (decisions.md #14). Seal happens on admin write; Open happens only in
// worker memory at run time. The implementation reuses the maintained
// AEAD-envelope primitive from pkg/keys — no hand-rolled crypto.
type CredSealer interface {
	// Seal wraps plaintext credentials for storage.
	Seal(c PlainCreds) (wrapped []byte, version int, err error)
	// Open unwraps stored credentials for immediate, in-memory use.
	Open(wrapped []byte) (PlainCreds, error)
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
