package config

// Takeout is the environment the admin's Take-Out extractor reads. Everything
// else it takes as flags; the credentials come from the environment because
// command lines are visible to every process on the host and land in shell
// history.
//
// No variable here is, or may become, key material: the tool cannot decrypt
// anything (decisions.md #2, #15).
type Takeout struct {
	S3 TakeoutS3 `section:"S3 credentials"`
}

// TakeoutS3 is the target's static credential. There is no ambient fallback
// (review-2026-10.md F5): both are required.
type TakeoutS3 struct {
	AccessKeyID     Secret `env:"S3_ACCESS_KEY_ID" doc:"Access key id for the backup target. Required."`
	SecretAccessKey Secret `env:"S3_SECRET_ACCESS_KEY" doc:"Secret for that access key id. Required."`
}

// LoadTakeout parses the extractor's environment. Whether the credentials are
// present is checked with the flags, so all usage errors read alike.
func LoadTakeout(environ []string) (Takeout, error) {
	var c Takeout
	if err := parse(environ, &c); err != nil {
		return Takeout{}, err
	}
	return c, nil
}
