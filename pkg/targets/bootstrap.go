package targets

// Optional first-start seeding of a single default target.
//
// With in-app, admin-managed targets (decisions.md #12) the target store is the
// source of truth and the admin UI is the normal way to populate it. Seeding
// exists so a fresh deployment is usable immediately — the "one click, then
// forget" promise — without the admin first opening the target UI.
//
// The seeded credentials are sealed with the TW key exactly like admin-entered
// ones (decisions.md #14): they are never persisted in the clear, never returned
// by any read path, and never logged. Seeding is skipped when any target already
// exists, so it can never overwrite what an admin configured.

import (
	"context"
	"fmt"
)

// BootstrapConfig describes the optional default target. Credentials come from
// a Secret, the rest from non-secret configuration.
type BootstrapConfig struct {
	// Enable gates the whole operation; false is the safe default.
	Enable bool
	// ID is the stable identifier of the seeded target.
	ID   string
	Name string

	Endpoint     string
	Region       string
	Bucket       string
	Prefix       string
	UsePathStyle bool
	DisableTLS   bool

	// Creds are the S3 credentials. They are sealed immediately and never
	// retained in this struct beyond the call.
	Creds PlainCreds
}

// DefaultBootstrapID is used when BootstrapConfig.ID is empty.
const DefaultBootstrapID = "default"

// Bootstrap seeds a single default target and grants it to all users, but only
// when seeding is enabled and the store holds no targets yet. It reports whether
// a target was created.
func Bootstrap(ctx context.Context, store Store, sealer CredSealer, cfg BootstrapConfig) (bool, error) {
	if !cfg.Enable {
		return false, nil
	}
	if store == nil || sealer == nil {
		return false, fmt.Errorf("targets: bootstrap requires a store and a credential sealer")
	}
	if cfg.Bucket == "" || cfg.Endpoint == "" {
		return false, fmt.Errorf("targets: bootstrap requires an endpoint and a bucket")
	}
	if cfg.Creds.AccessKeyID == "" || cfg.Creds.SecretAccessKey == "" {
		return false, fmt.Errorf("targets: bootstrap requires S3 credentials")
	}

	existing, err := store.ListTargets(ctx)
	if err != nil {
		return false, fmt.Errorf("targets: bootstrap: read targets: %w", err)
	}
	if len(existing) > 0 {
		// An admin has already configured targets; never override them.
		return false, nil
	}

	wrapped, version, err := sealer.Seal(cfg.Creds)
	if err != nil {
		// Deliberately generic: never echo credential content.
		return false, fmt.Errorf("targets: bootstrap: cannot seal credentials")
	}

	id := cfg.ID
	if id == "" {
		id = DefaultBootstrapID
	}
	name := cfg.Name
	if name == "" {
		name = "Buddy-S3"
	}

	if _, err := store.CreateTarget(ctx, Target{
		ID:           id,
		Name:         name,
		Endpoint:     cfg.Endpoint,
		Region:       cfg.Region,
		Bucket:       cfg.Bucket,
		Prefix:       cfg.Prefix,
		UsePathStyle: cfg.UsePathStyle,
		DisableTLS:   cfg.DisableTLS,
		WrappedCreds: wrapped,
		Version:      version,
	}); err != nil {
		return false, fmt.Errorf("targets: bootstrap: create target: %w", err)
	}

	// A single seeded target is granted to everyone: with exactly one target the
	// user experience stays "one click" (decisions.md #12).
	if err := store.PutGrant(ctx, Grant{TargetID: id, Scope: ScopeAllUsers}); err != nil {
		return false, fmt.Errorf("targets: bootstrap: grant target: %w", err)
	}
	return true, nil
}
