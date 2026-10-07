package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"opencloud-backup-plugin/internal/cli"
)

func TestParseProvision(t *testing.T) {
	cfg := testConfig(t)
	// Without a gateway the action fails at once, naming the variable; that
	// is enough to see which name it was parsed with.
	for _, args := range [][]string{nil, {"-name", "Service state"}} {
		act, err := parseProvision(args, io.Discard, io.Discard)
		if err != nil {
			t.Fatalf("parseProvision(%v): %v", args, err)
		}
		if err := act(context.Background(), cfg, quietLogger(), io.Discard); err == nil {
			t.Fatal("provisioning ran without a gateway")
		}
	}

	for _, args := range [][]string{{"-name", ""}, {"unexpected"}, {"-nope"}} {
		if _, err := parseProvision(args, io.Discard, io.Discard); cli.ExitCode(err) != cli.ExitUsage {
			t.Errorf("parseProvision(%v) = %v, want a usage error", args, err)
		}
	}
}

// The command needs the service account, because whoever creates the Space
// keeps the one grant OpenCloud will not remove. Failing before any network
// call keeps a half-provisioned Space from existing.
func TestProvisionStateSpace_RefusesWithoutAGateway(t *testing.T) {
	err := runProvisionStateSpace(context.Background(), testConfig(t), defaultStateSpaceName, quietLogger(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "CS3_GATEWAY_ADDR is required") {
		t.Fatalf("err = %v", err)
	}
}

// Without the service account the configuration itself is refused, before the
// command runs (internal/config).
func TestProvisionStateSpace_RefusesWithoutTheServiceAccount(t *testing.T) {
	for name, environ := range map[string][]string{
		"no service account":     {"CS3_GATEWAY_ADDR=127.0.0.1:9142"},
		"half a service account": {"CS3_GATEWAY_ADDR=127.0.0.1:9142", "OC_SERVICE_ACCOUNT_ID=svc"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run([]string{"provision-state-space"}, environ, &stdout, &stderr); code != 1 {
				t.Fatalf("exit code = %d", code)
			}
			if !strings.Contains(stderr.String(), "OC_SERVICE_ACCOUNT_SECRET are required") {
				t.Fatalf("stderr = %s", stderr.String())
			}
		})
	}
}

// A second state Space is the failure this guard exists for: the first one
// holds every wrapped Data Key, and nothing would point at it any more.
func TestProvisionStateSpace_RefusesWhenAlreadyConfigured(t *testing.T) {
	cfg := testConfig(t,
		"CS3_GATEWAY_ADDR=127.0.0.1:9142",
		"OC_SERVICE_ACCOUNT_ID=svc",
		"OC_SERVICE_ACCOUNT_SECRET=s",
		"STATE_SPACE_ID=already-provisioned",
	)
	err := runProvisionStateSpace(context.Background(), cfg, defaultStateSpaceName, quietLogger(), io.Discard)
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
	err := runCommand(t, testConfig(t), "provision")
	if err == nil {
		t.Fatal("an unknown command must fail")
	}
	for _, want := range []string{"provision-state-space", "rotate-srw", "rotate-tw", "version", "help"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to mention %q", err, want)
		}
	}
}
