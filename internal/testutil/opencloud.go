package testutil

// Access to the live OpenCloud fixture (test/fixtures/opencloud), and the rule
// that decides what a missing fixture means.
//
// A test that needs OpenCloud has to do something when there is none. On a
// developer's machine "skip" is right — not everyone has the fixture up, and a
// unit-test run should not fail because of it. In CI it is exactly wrong: the
// job exists to run these tests, and a skipped test is indistinguishable from a
// passing one in a summary. Before this, every OpenCloud test skipped in CI and
// the run was green (review-2026-09.md F7).
//
// So the caller does not decide; the environment does. The CI job that starts
// the fixture sets OPENCLOUD_FIXTURE_REQUIRED, and from then on a missing
// fixture is a failure with the name of what was missing.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// FixtureRequiredVar turns a skipped OpenCloud test into a failed one. The CI
// job that brings the fixture up sets it; nothing else should.
const FixtureRequiredVar = "OPENCLOUD_FIXTURE_REQUIRED"

// OpenCloudEnv returns the named fixture variables in order, stopping the test
// if any of them is empty.
//
//	addr, id, secret := testutil.OpenCloudEnv(t,
//		"CS3_GATEWAY_ADDR", "CS3_SERVICE_ACCOUNT_ID", "CS3_SERVICE_ACCOUNT_SECRET")
func OpenCloudEnv(t *testing.T, names ...string) []string {
	t.Helper()

	values := make([]string, len(names))
	var missing []string
	for i, name := range names {
		values[i] = strings.TrimSpace(os.Getenv(name))
		if values[i] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return values
	}

	stop(t, missingFixtureMessage(missing))
	return nil
}

// FixtureGap stops a test that started, reached the fixture, and found it
// incomplete — a Space that is not there, a file seed.sh did not write. Same
// rule as OpenCloudEnv: a gap is a skip locally and a failure where the fixture
// was promised.
func FixtureGap(t *testing.T, format string, args ...any) {
	t.Helper()
	stop(t, fmt.Sprintf(format, args...)+"; re-run test/fixtures/opencloud/seed.sh")
}

// FixtureRequired reports whether a missing fixture must fail the run.
func FixtureRequired() bool {
	return truthy(os.Getenv(FixtureRequiredVar))
}

func stop(t *testing.T, message string) {
	t.Helper()
	if FixtureRequired() {
		t.Fatalf("%s (%s is set, so this must not be skipped)", message, FixtureRequiredVar)
	}
	t.Skip(message)
}

// missingFixtureMessage names what was missing and how to get it. The names
// matter: "fixture env not set" sends a reader to the wrong file half the time,
// because seed.sh appends variables that up.sh never writes.
func missingFixtureMessage(missing []string) string {
	return fmt.Sprintf(
		"OpenCloud fixture env not set (%s); run test/fixtures/opencloud/up.sh and seed.sh, "+
			"then source test/fixtures/opencloud/fixture.env", strings.Join(missing, ", "))
}

// truthy reads the flag the way an operator would expect: 1, true, yes, on.
func truthy(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "yes" || value == "on" {
		return true
	}
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}
