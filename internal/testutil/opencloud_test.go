package testutil

import (
	"strings"
	"testing"
)

// The whole point of the guard is the difference between "no fixture here" and
// "the fixture was promised", so the flag that distinguishes them is worth a
// test of its own.
func TestFixtureRequiredFlag(t *testing.T) {
	cases := map[string]bool{
		"":      false,
		"0":     false,
		"false": false,
		"no":    false,
		"1":     true,
		"true":  true,
		"TRUE":  true,
		"yes":   true,
		"on":    true,
		" 1 ":   true,
		"maybe": false,
	}
	for value, want := range cases {
		t.Setenv(FixtureRequiredVar, value)
		if got := FixtureRequired(); got != want {
			t.Errorf("%s=%q -> %v, want %v", FixtureRequiredVar, value, got, want)
		}
	}
}

// A skip nobody can act on is barely better than a silent one.
func TestMissingFixtureMessageNamesTheVariables(t *testing.T) {
	msg := missingFixtureMessage([]string{"CS3_GATEWAY_ADDR", "OC_SHARED_SPACE_ID"})
	for _, want := range []string{"CS3_GATEWAY_ADDR", "OC_SHARED_SPACE_ID", "up.sh", "seed.sh", "fixture.env"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not mention %q: %s", want, msg)
		}
	}
}

func TestOpenCloudEnvReturnsValuesInOrder(t *testing.T) {
	t.Setenv("OC_TEST_ONE", "first")
	t.Setenv("OC_TEST_TWO", "second")

	got := OpenCloudEnv(t, "OC_TEST_ONE", "OC_TEST_TWO")
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("OpenCloudEnv = %v", got)
	}
}
