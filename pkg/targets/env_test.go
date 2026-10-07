package targets

import (
	"strings"
	"testing"

	"opencloud-backup-plugin/internal/config"
)

func completeBootstrapEnv() []string {
	return []string{
		"BOOTSTRAP_ENABLE=true",
		"BOOTSTRAP_TARGET_NAME=Buddy",
		"BOOTSTRAP_S3_ENDPOINT=s3.example:3900",
		"BOOTSTRAP_S3_BUCKET=backups",
		"BOOTSTRAP_S3_USE_PATH_STYLE=true",
		"BOOTSTRAP_S3_ACCESS_KEY_ID=id",
		"BOOTSTRAP_S3_SECRET_ACCESS_KEY=secret",
	}
}

func loadBootstrapEnv(environ ...string) (BootstrapEnv, error) {
	var e BootstrapEnv
	err := config.Load(environ, &e)
	return e, err
}

func TestBootstrapEnv_Config(t *testing.T) {
	e, err := loadBootstrapEnv(completeBootstrapEnv()...)
	if err != nil {
		t.Fatal(err)
	}
	cfg := e.Config()
	if !cfg.Enable || cfg.Name != "Buddy" || cfg.Bucket != "backups" || !cfg.UsePathStyle ||
		cfg.Creds != (PlainCreds{AccessKeyID: "id", SecretAccessKey: "secret"}) || !cfg.MaintenanceCreds.Empty() {
		t.Fatalf("Config = %+v", cfg)
	}
}

// An enabled seeding that cannot seed is refused at startup, naming what is
// missing.
func TestBootstrapEnv_RefusesAnIncompleteTarget(t *testing.T) {
	for name, tc := range map[string]struct {
		drop, add string
		want      string
	}{
		"no bucket":         {drop: "BOOTSTRAP_S3_BUCKET", want: "endpoint and a bucket"},
		"no endpoint":       {drop: "BOOTSTRAP_S3_ENDPOINT", want: "endpoint and a bucket"},
		"no secret":         {drop: "BOOTSTRAP_S3_SECRET_ACCESS_KEY", want: "S3 credentials"},
		"half maintenance":  {add: "BOOTSTRAP_S3_MAINTENANCE_ACCESS_KEY_ID=m", want: "maintenance credential"},
		"nothing but a yes": {drop: "*", want: "BOOTSTRAP_ENABLE is true"},
	} {
		t.Run(name, func(t *testing.T) {
			var environ []string
			for _, entry := range completeBootstrapEnv() {
				keep := tc.drop != "*" && !strings.HasPrefix(entry, tc.drop+"=")
				if keep || entry == "BOOTSTRAP_ENABLE=true" {
					environ = append(environ, entry)
				}
			}
			if tc.add != "" {
				environ = append(environ, tc.add)
			}
			_, err := loadBootstrapEnv(environ...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// Switched off, the rest is not looked at: a manifest may keep a prepared
// target around without seeding it.
func TestBootstrapEnv_DisabledIsNotChecked(t *testing.T) {
	if _, err := loadBootstrapEnv("BOOTSTRAP_S3_BUCKET=backups"); err != nil {
		t.Fatal(err)
	}
}
