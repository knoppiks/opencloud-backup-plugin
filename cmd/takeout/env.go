package main

import envconfig "opencloud-backup-plugin/internal/config"

// takeoutEnv is the environment the extractor reads. Everything else it takes
// as flags; the credentials come from the environment because command lines
// are visible to every process on the host and land in shell history.
//
// Declared here rather than in pkg/takeout, which is public (decisions.md,
// 10.7). No variable here is, or may become, key material: the tool cannot
// decrypt anything (decisions.md #2, #15).
type takeoutEnv struct {
	S3 s3CredentialsEnv `section:"S3 credentials"`
}

// s3CredentialsEnv is the target's static credential. There is no ambient
// fallback (review-2026-10.md F5): both are required, which validate checks
// with the flags so all usage errors read alike.
type s3CredentialsEnv struct {
	AccessKeyID     envconfig.Secret `env:"S3_ACCESS_KEY_ID" doc:"Access key id for the backup target. Required."`
	SecretAccessKey envconfig.Secret `env:"S3_SECRET_ACCESS_KEY" doc:"Secret for that access key id. Required."`
}

// loadTakeoutEnv parses the extractor's environment.
func loadTakeoutEnv(environ []string) (takeoutEnv, error) {
	var env takeoutEnv
	if err := envconfig.Load(environ, &env); err != nil {
		return takeoutEnv{}, err
	}
	return env, nil
}
