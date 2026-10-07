package targets

import (
	"fmt"

	"opencloud-backup-plugin/internal/config"
)

// BootstrapEnv is the optional default target, as the environment declares
// it (internal/config). It is seeded on first start, when no target exists
// yet, and never overrides what an admin configured.
type BootstrapEnv struct {
	Enable                     bool          `env:"BOOTSTRAP_ENABLE" doc:"Seed the target below on start, if no target exists yet. Needs TW_KEY."`
	TargetID                   string        `env:"BOOTSTRAP_TARGET_ID" doc:"Id of the seeded target. Unset, a fixed default id."`
	TargetName                 string        `env:"BOOTSTRAP_TARGET_NAME" doc:"Display name of the seeded target."`
	Endpoint                   string        `env:"BOOTSTRAP_S3_ENDPOINT" doc:"S3 endpoint, host:port or URL."`
	Region                     string        `env:"BOOTSTRAP_S3_REGION" doc:"S3 region."`
	Bucket                     string        `env:"BOOTSTRAP_S3_BUCKET" doc:"S3 bucket."`
	Prefix                     string        `env:"BOOTSTRAP_S3_PREFIX" doc:"Prefix inside the bucket."`
	UsePathStyle               bool          `env:"BOOTSTRAP_S3_USE_PATH_STYLE" doc:"Path-style addressing (Garage and most self-hosted S3 need it)."`
	DisableTLS                 bool          `env:"BOOTSTRAP_S3_DISABLE_TLS" doc:"Plain HTTP to the endpoint."`
	AccessKeyID                config.Secret `env:"BOOTSTRAP_S3_ACCESS_KEY_ID" doc:"Access key id backups write with."`
	SecretAccessKey            config.Secret `env:"BOOTSTRAP_S3_SECRET_ACCESS_KEY" doc:"Secret for that access key id."`
	MaintenanceAccessKeyID     config.Secret `env:"BOOTSTRAP_S3_MAINTENANCE_ACCESS_KEY_ID" doc:"Optional second access key id, used by prune runs only. Set both halves or neither."`
	MaintenanceSecretAccessKey config.Secret `env:"BOOTSTRAP_S3_MAINTENANCE_SECRET_ACCESS_KEY" doc:"Secret for the maintenance access key id."`
}

var _ config.Validator = (*BootstrapEnv)(nil)

// Validate refuses an enabled seeding that could not seed anything, at
// startup rather than after the service has claimed its instance record.
func (e *BootstrapEnv) Validate() error {
	if !e.Enable {
		return nil
	}
	if err := e.Config().Validate(); err != nil {
		return fmt.Errorf("BOOTSTRAP_ENABLE is true but the BOOTSTRAP_S3_* target is incomplete: %w", err)
	}
	return nil
}

// Config is the seeding configuration from e. The credentials must come from
// a Secret (decisions.md #14) and are handed straight to the sealer.
func (e BootstrapEnv) Config() BootstrapConfig {
	return BootstrapConfig{
		Enable:       e.Enable,
		ID:           e.TargetID,
		Name:         e.TargetName,
		Endpoint:     e.Endpoint,
		Region:       e.Region,
		Bucket:       e.Bucket,
		Prefix:       e.Prefix,
		UsePathStyle: e.UsePathStyle,
		DisableTLS:   e.DisableTLS,
		Creds: PlainCreds{
			AccessKeyID:     e.AccessKeyID.Reveal(),
			SecretAccessKey: e.SecretAccessKey.Reveal(),
		},
		// Optional second credential, used only by prune runs (decisions.md
		// #9, Tier 2). Unset means the deployment has one key and both roles
		// use it; half-set is refused rather than silently falling back.
		MaintenanceCreds: PlainCreds{
			AccessKeyID:     e.MaintenanceAccessKeyID.Reveal(),
			SecretAccessKey: e.MaintenanceSecretAccessKey.Reveal(),
		},
	}
}
