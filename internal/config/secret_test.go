package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

// Every way a Secret, or a struct holding one, is likely to be printed.
var formats = []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%T"}

func TestSecret_NeverPrintsItsValue(t *testing.T) {
	value := "sentinel-" + wrapKey(t)
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

// secretEnv sets every Secret variable of cfg to a distinct random value and
// every other variable to something valid, returning the environment and the
// secret values.
func secretEnv(t *testing.T, cfg any) (environ, secrets []string) {
	t.Helper()
	for _, v := range variables(reflect.ValueOf(cfg).Elem()) {
		if v.typ == secretType {
			value := wrapKey(t)
			environ = append(environ, v.env+"="+value)
			secrets = append(secrets, value)
		}
	}
	return environ, secrets
}

// The effective-configuration line holds every variable and no secret value,
// and redaction is by type: no list of names is consulted.
func TestBackupd_LogValueRedactsSecretsByType(t *testing.T) {
	environ, secrets := secretEnv(t, &Backupd{})
	environ = append(environ, "OC_BASE_URL=https://cloud.example")
	// The two custody keys must differ, and secretEnv made them so.
	c := mustLoad(t, environ...)

	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("configuration", "config", c)
	line := buf.String()

	for _, value := range secrets {
		if strings.Contains(line, value) {
			t.Fatalf("the configuration line carries a secret value: %s", line)
		}
	}
	if !strings.Contains(line, `"oc_base_url":"https://cloud.example"`) {
		t.Errorf("a plain value is missing: %s", line)
	}
	if !strings.Contains(line, `"srw_key":"`+redacted+`"`) {
		t.Errorf("a set secret is not shown as set: %s", line)
	}

	var decoded struct{ Config map[string]any }
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, v := range variables(reflect.ValueOf(&Backupd{}).Elem()) {
		if _, ok := decoded.Config[strings.ToLower(v.env)]; !ok {
			t.Errorf("%s is missing from the configuration line", v.env)
		}
	}
}

func TestBackupd_FormattingDoesNotLeakSecrets(t *testing.T) {
	environ, secrets := secretEnv(t, &Backupd{})
	c := mustLoad(t, environ...)
	for _, format := range formats {
		out := fmt.Sprintf(format, c)
		for _, value := range secrets {
			if strings.Contains(out, value) {
				t.Fatalf("Sprintf(%q) leaks a secret", format)
			}
		}
	}
}

// Whatever a deployment takes from a Kubernetes Secret is a Secret here: that
// is the rule redaction rests on, so a credential declared as a plain string
// is a bug.
func TestCredentialsAreSecrets(t *testing.T) {
	for _, cfg := range []any{&Backupd{}, &Takeout{}} {
		for _, v := range variables(reflect.ValueOf(cfg).Elem()) {
			name := v.env
			credential := strings.Contains(name, "SECRET") || strings.Contains(name, "PASSWORD") ||
				strings.HasSuffix(name, "_KEY") || strings.HasSuffix(name, "_KEY_OLD") ||
				strings.Contains(name, "ACCESS_KEY") || name == "OC_SERVICE_ACCOUNT_ID"
			if credential && v.typ != secretType {
				t.Errorf("%s is a credential but not declared as a Secret", name)
			}
		}
	}
}
