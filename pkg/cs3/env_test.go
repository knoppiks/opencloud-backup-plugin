package cs3

import (
	"strings"
	"testing"

	"opencloud-backup-plugin/internal/config"
)

func loadEnv(environ ...string) (Env, error) {
	var e Env
	err := config.Load(environ, &e)
	return e, err
}

// The service used to start without the service account and fail on its
// first gateway call; only the provisioning command checked.
func TestEnv_GatewayNeedsTheServiceAccount(t *testing.T) {
	for name, environ := range map[string][]string{
		"neither":    {"CS3_GATEWAY_ADDR=gw:9142"},
		"no secret":  {"CS3_GATEWAY_ADDR=gw:9142", "OC_SERVICE_ACCOUNT_ID=svc"},
		"no account": {"CS3_GATEWAY_ADDR=gw:9142", "OC_SERVICE_ACCOUNT_SECRET=s"},
	} {
		_, err := loadEnv(environ...)
		if err == nil || !strings.Contains(err.Error(), "OC_SERVICE_ACCOUNT_ID and OC_SERVICE_ACCOUNT_SECRET are required") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := loadEnv("CS3_GATEWAY_ADDR=gw:9142", "OC_SERVICE_ACCOUNT_ID=svc", "OC_SERVICE_ACCOUNT_SECRET=s"); err != nil {
		t.Fatal(err)
	}
	// Without a gateway nothing dials, so nothing is required.
	if _, err := loadEnv(); err != nil {
		t.Fatal(err)
	}
}

// A bad data server URL is refused at startup now, not at the first dial.
func TestEnv_DataServerURL(t *testing.T) {
	e, err := loadEnv("CS3_DATA_SERVER_URL=http://opencloud.files.svc.cluster.local:9158")
	if err != nil {
		t.Fatal(err)
	}
	if origin, err := e.DataServerOrigin(); err != nil || origin.Host != "opencloud.files.svc.cluster.local:9158" {
		t.Fatalf("origin = %v, %v", origin, err)
	}

	// A path would be silently ignored, so it is refused rather than accepted.
	if _, err := loadEnv("CS3_DATA_SERVER_URL=http://opencloud:9158/data"); err == nil ||
		!strings.Contains(err.Error(), "CS3_DATA_SERVER_URL") {
		t.Fatalf("with a path: err = %v, want a refusal naming the variable", err)
	}

	if origin, err := (Env{}).DataServerOrigin(); origin != nil || err != nil {
		t.Fatalf("unset = %v, %v", origin, err)
	}
}
