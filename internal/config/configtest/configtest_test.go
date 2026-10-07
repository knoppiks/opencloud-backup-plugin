package configtest

import (
	"os"
	"path/filepath"
	"testing"

	"opencloud-backup-plugin/internal/config"
)

type sample struct {
	Addr     string        `env:"T_ADDR" doc:"Address."`
	Password config.Secret `env:"T_PASSWORD" doc:"Password."`
}

func TestCheckDeclarations_PassesAWellDeclaredRoot(t *testing.T) {
	CheckDeclarations(t, func() any { return &sample{Addr: ":8080"} })
}

func TestLooksLikeCredential(t *testing.T) {
	for name, want := range map[string]bool{
		"SMTP_PASSWORD": true, "SRW_KEY": true, "TW_KEY_OLD": true, "S3_ACCESS_KEY_ID": true,
		"OC_SERVICE_ACCOUNT_SECRET": true, "OC_SERVICE_ACCOUNT_ID": true,
		"BACKUPD_ADDR": false, "TLS_KEY_FILE": false,
	} {
		if got := looksLikeCredential(name); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

func TestCheckReference_AcceptsWhatItGenerates(t *testing.T) {
	p := config.Program{Name: "sample", Intro: "Intro.", Config: &sample{}, Generator: "make generate"}
	path := filepath.Join(t.TempDir(), "ref.md")
	if err := os.WriteFile(path, config.Reference(p), 0o644); err != nil {
		t.Fatal(err)
	}
	CheckReference(t, p, path)
}
