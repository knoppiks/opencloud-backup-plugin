package main

import (
	"path/filepath"
	"testing"

	envconfig "opencloud-backup-plugin/internal/config"
	"opencloud-backup-plugin/internal/config/configtest"
)

func TestTakeoutEnv_Declarations(t *testing.T) {
	configtest.CheckDeclarations(t, func() any { return &takeoutEnv{} })
}

// The checked-in reference is what the declarations generate. When this
// fails, run make generate and commit the result.
func TestEnvironmentReference(t *testing.T) {
	configtest.CheckReference(t, envconfig.Program{
		Name: "takeout",
		Intro: "The admin's Take-Out extractor. Everything else it takes as flags; " +
			"no setting of it accepts key material.",
		Config:    &takeoutEnv{},
		Generator: "make generate",
	}, filepath.Join("..", "..", "docs", "reference", "environment-takeout.md"))
}

func TestLoadTakeoutEnv(t *testing.T) {
	env, err := loadTakeoutEnv([]string{"S3_ACCESS_KEY_ID=id", "S3_SECRET_ACCESS_KEY=secret"})
	if err != nil {
		t.Fatal(err)
	}
	if env.S3.AccessKeyID.Reveal() != "id" || env.S3.SecretAccessKey.Reveal() != "secret" {
		t.Fatalf("env = %+v", env)
	}
	if _, err := loadTakeoutEnv([]string{"S3_ACCESS_KEY_ID=REPLACE_ME"}); err == nil {
		t.Fatal("a placeholder credential was accepted")
	}
	if env, err := loadTakeoutEnv(nil); err != nil || env.S3.AccessKeyID.IsSet() {
		t.Fatalf("empty = %+v, %v", env, err)
	}
}
