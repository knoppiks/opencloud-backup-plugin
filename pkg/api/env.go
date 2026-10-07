package api

import (
	"errors"

	"opencloud-backup-plugin/internal/config"
)

// Env is the API's identity configuration, as the environment declares it:
// whose tokens it accepts, and where it asks who a caller is (internal/config).
type Env struct {
	OIDC      OIDCEnv      `section:"Authentication"`
	OpenCloud OpenCloudEnv `section:"OpenCloud"`
}

// OIDCEnv validates the bearer tokens the web extension sends.
type OIDCEnv struct {
	Issuer   string `env:"OIDC_ISSUER" doc:"OpenID Connect issuer whose tokens are accepted. Unset, every authenticated route refuses. Requires OIDC_AUDIENCE and OC_BASE_URL."`
	Audience string `env:"OIDC_AUDIENCE" doc:"Client id the tokens must be issued for. Without it any token the issuer minted for any application would be accepted."`
}

// OpenCloudEnv is the Graph API side: who a caller is, their groups, their
// role.
type OpenCloudEnv struct {
	BaseURL        string   `env:"OC_BASE_URL" doc:"OpenCloud's public URL. A caller's user id, groups and admin role are read from its Graph API, and its version from its status endpoint."`
	AdminAppRoleID string   `env:"OC_ADMIN_APP_ROLE_ID" doc:"Id of OpenCloud's admin app role. The default is OpenCloud's built-in Admin role."`
	AdminAllowlist []string `env:"ADMIN_SUBJECT_ALLOWLIST" doc:"OpenCloud user ids that are backup admins, instead of asking Graph for the admin role. Despite the name these are user ids, not token subjects."`
}

// DefaultEnv is the configuration of an unset environment.
func DefaultEnv() Env {
	return Env{OpenCloud: OpenCloudEnv{AdminAppRoleID: DefaultAdminAppRoleID}}
}

var _ config.Validator = (*Env)(nil)

// Validate refuses an issuer the API could not use safely.
func (e *Env) Validate() error {
	if e.OIDC.Issuer == "" {
		return nil
	}
	var errs []error
	if e.OIDC.Audience == "" {
		errs = append(errs, errors.New(
			"OIDC_AUDIENCE is required when OIDC_ISSUER is set: without it any token "+
				"the same issuer minted for any application is accepted here"))
	}
	// The token's `sub` is not the OpenCloud user id, and every authorization
	// decision is taken on the latter (users.go).
	if e.OpenCloud.BaseURL == "" {
		errs = append(errs, errors.New(
			"OC_BASE_URL is required when OIDC_ISSUER is set: a caller's OpenCloud "+
				"user id is read from its graph API, and the token's subject is not that id"))
	}
	return errors.Join(errs...)
}
