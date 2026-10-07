package api

import (
	"strings"
	"testing"

	"opencloud-backup-plugin/internal/config"
)

func loadEnv(environ ...string) (Env, error) {
	e := DefaultEnv()
	err := config.Load(environ, &e)
	return e, err
}

func TestEnv_Defaults(t *testing.T) {
	e, err := loadEnv()
	if err != nil {
		t.Fatal(err)
	}
	if e.OpenCloud.AdminAppRoleID != DefaultAdminAppRoleID {
		t.Errorf("AdminAppRoleID = %q", e.OpenCloud.AdminAppRoleID)
	}
	if e.OIDC.Issuer != "" || len(e.OpenCloud.AdminAllowlist) != 0 {
		t.Errorf("Env = %+v", e)
	}
}

func TestEnv_IssuerNeedsAudienceAndOpenCloud(t *testing.T) {
	_, err := loadEnv("OIDC_ISSUER=https://issuer.invalid")
	for _, name := range []string{"OIDC_AUDIENCE", "OC_BASE_URL"} {
		if err == nil || !strings.Contains(err.Error(), name+" is required") {
			t.Errorf("error does not require %s: %v", name, err)
		}
	}
	if _, err := loadEnv("OIDC_ISSUER=https://issuer.invalid", "OIDC_AUDIENCE=web", "OC_BASE_URL=https://cloud.example"); err != nil {
		t.Fatal(err)
	}
}

func TestEnv_ReadsTheAllowlist(t *testing.T) {
	e, err := loadEnv("ADMIN_SUBJECT_ALLOWLIST=a, b")
	if err != nil || len(e.OpenCloud.AdminAllowlist) != 2 {
		t.Fatalf("Env = %+v, %v", e, err)
	}
}
