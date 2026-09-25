package main

import (
	"context"
	"strings"
	"testing"
)

func TestParseProvisionFlags(t *testing.T) {
	got, err := parseProvisionFlags(nil)
	if err != nil {
		t.Fatalf("parseProvisionFlags(nil): %v", err)
	}
	if got != defaultStateSpaceName {
		t.Fatalf("default name = %q, want %q", got, defaultStateSpaceName)
	}

	got, err = parseProvisionFlags([]string{"-name", "Service state"})
	if err != nil {
		t.Fatalf("parseProvisionFlags: %v", err)
	}
	if got != "Service state" {
		t.Fatalf("name = %q", got)
	}

	if _, err := parseProvisionFlags([]string{"-name", ""}); err == nil {
		t.Fatal("an empty name must be refused")
	}
	if _, err := parseProvisionFlags([]string{"unexpected"}); err == nil {
		t.Fatal("a positional argument must be refused")
	}
}

// The command needs the service account, because whoever creates the Space
// keeps the one grant OpenCloud will not remove. Failing before any network
// call keeps a half-provisioned Space from existing.
func TestProvisionStateSpace_RefusesIncompleteConfiguration(t *testing.T) {
	cases := map[string]struct {
		env     map[string]string
		wantErr string
	}{
		"no gateway": {
			env:     map[string]string{"CS3_GATEWAY_ADDR": ""},
			wantErr: "CS3_GATEWAY_ADDR is required",
		},
		"no service account": {
			env: map[string]string{
				"CS3_GATEWAY_ADDR":          "127.0.0.1:9142",
				"OC_SERVICE_ACCOUNT_ID":     "",
				"OC_SERVICE_ACCOUNT_SECRET": "",
			},
			wantErr: "OC_SERVICE_ACCOUNT_ID",
		},
		"half a service account": {
			env: map[string]string{
				"CS3_GATEWAY_ADDR":          "127.0.0.1:9142",
				"OC_SERVICE_ACCOUNT_ID":     "svc",
				"OC_SERVICE_ACCOUNT_SECRET": "",
			},
			wantErr: "OC_SERVICE_ACCOUNT_SECRET",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("STATE_SPACE_ID", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			err := runProvisionStateSpace(context.Background(), nil, quietLogger())
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// A second state Space is the failure this guard exists for: the first one
// holds every wrapped Data Key, and nothing would point at it any more.
func TestProvisionStateSpace_RefusesWhenAlreadyConfigured(t *testing.T) {
	t.Setenv("CS3_GATEWAY_ADDR", "127.0.0.1:9142")
	t.Setenv("OC_SERVICE_ACCOUNT_ID", "svc")
	t.Setenv("OC_SERVICE_ACCOUNT_SECRET", "secret")
	t.Setenv("STATE_SPACE_ID", "already-provisioned")

	err := runProvisionStateSpace(context.Background(), nil, quietLogger())
	if err == nil {
		t.Fatal("provisioning must be refused while STATE_SPACE_ID is set")
	}
	if !strings.Contains(err.Error(), "STATE_SPACE_ID is already set") {
		t.Fatalf("err = %v", err)
	}
}

// An operator who mistypes a command must be told what exists, and must not
// have it interpreted as something else.
func TestRunCommand_UnknownCommandNamesTheKnownOnes(t *testing.T) {
	err := runCommand(context.Background(), "provision", nil, quietLogger())
	if err == nil {
		t.Fatal("an unknown command must fail")
	}
	for _, want := range []string{"provision-state-space", "rotate-srw", "rotate-tw"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to mention %q", err, want)
		}
	}
}
