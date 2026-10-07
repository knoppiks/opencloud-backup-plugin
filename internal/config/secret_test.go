package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Every way a Secret, or a struct holding one, is likely to be printed.
var formats = []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%T"}

// randomValue returns a fresh random string. Generated at runtime: a
// key-shaped literal in a test is indistinguishable from a leaked one.
func randomValue(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestSecret_NeverPrintsItsValue(t *testing.T) {
	value := "sentinel-" + randomValue(t)
	s := NewSecret(value)
	holder := struct{ S Secret }{s}
	for _, format := range formats {
		for _, arg := range []any{s, &s, holder, &holder} {
			if out := fmt.Sprintf(format, arg); strings.Contains(out, value) {
				t.Errorf("Sprintf(%q, %T) leaks the value: %s", format, arg, out)
			}
		}
	}
	out, err := json.Marshal(holder)
	if err != nil || strings.Contains(string(out), value) {
		t.Errorf("json = %s, %v", out, err)
	}
	if s.Reveal() != value {
		t.Error("Reveal does not return the value")
	}
}

func TestSecret_SaysWhetherItIsSet(t *testing.T) {
	if got := NewSecret("x").String(); got != redacted {
		t.Errorf("set = %q", got)
	}
	if got := NewSecret("").String(); got != "" {
		t.Errorf("unset = %q", got)
	}
	if NewSecret("").IsSet() || (Secret{}).IsSet() {
		t.Error("an empty Secret reads as set")
	}
}
