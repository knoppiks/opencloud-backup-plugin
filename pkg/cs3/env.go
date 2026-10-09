package cs3

import (
	"errors"
	"fmt"
	"net/url"

	"opencloud-backup-plugin/internal/config"
)

// Env is how this service reaches OpenCloud's CS3 gateway, as the environment
// declares it (internal/config).
type Env struct {
	GatewayAddr          string        `env:"CS3_GATEWAY_ADDR" doc:"OpenCloud's CS3 gateway, host:port (plaintext gRPC, cluster network only). Unset, the service starts without Spaces, backups or durable state. Requires OC_SERVICE_ACCOUNT_ID and OC_SERVICE_ACCOUNT_SECRET."`
	DataServerURL        string        `env:"CS3_DATA_SERVER_URL" doc:"Where OpenCloud's storage data server is reachable from this service, scheme and host only. Needed from OpenCloud 7.5 on; harmless before."`
	ServiceAccountID     config.Secret `env:"OC_SERVICE_ACCOUNT_ID" doc:"Id of the OpenCloud service account the service acts as."`
	ServiceAccountSecret config.Secret `env:"OC_SERVICE_ACCOUNT_SECRET" doc:"Secret of that service account. It reaches every Space's plaintext: the most valuable credential in the deployment."`
}

var _ config.Validator = (*Env)(nil)

// Validate refuses a gateway the service could not authenticate to, and a
// data server URL it could not use.
func (e *Env) Validate() error {
	var errs []error
	// Every CS3 call is made as the service account. Without it the service
	// used to start, pass its readiness probe on nothing, and fail on the
	// first gateway call with an authentication error that named neither
	// variable.
	if e.GatewayAddr != "" && (!e.ServiceAccountID.IsSet() || !e.ServiceAccountSecret.IsSet()) {
		errs = append(errs, errors.New(
			"OC_SERVICE_ACCOUNT_ID and OC_SERVICE_ACCOUNT_SECRET are required when CS3_GATEWAY_ADDR "+
				"is set: every CS3 call is made as the service account"))
	}
	if _, err := e.DataServerOrigin(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// DataServerOrigin is CS3_DATA_SERVER_URL parsed, or nil when it is unset.
func (e Env) DataServerOrigin() (*url.URL, error) {
	if e.DataServerURL == "" {
		return nil, nil
	}
	origin, err := ParseDataServerOrigin(e.DataServerURL)
	if err != nil {
		return nil, fmt.Errorf("CS3_DATA_SERVER_URL: %w", err)
	}
	return origin, nil
}
