// Package configtest holds the checks every binary's configuration must pass,
// so each binary states them in one line rather than copying them.
package configtest

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	"opencloud-backup-plugin/internal/config"
)

var update = flag.Bool("update", false, "rewrite the generated environment reference")

// CheckDeclarations checks the configuration root newRoot returns (a pointer
// to a struct holding the defaults, fresh on every call):
//
//   - every variable is documented, and declared once;
//   - every credential is a Secret, and no Secret reaches the effective
//     configuration, a formatted root, or a load error;
//   - the manifest's placeholder is refused in every variable, alone and
//     without repeating the value.
func CheckDeclarations(t *testing.T, newRoot func() any) {
	t.Helper()
	vars := config.Describe(newRoot())
	if len(vars) == 0 {
		t.Fatal("no variables declared; these checks check nothing")
	}

	t.Run("documented once", func(t *testing.T) {
		seen := map[string]bool{}
		for _, v := range vars {
			if v.Doc == "" {
				t.Errorf("%s has no doc tag", v.Name)
			}
			if seen[v.Name] {
				t.Errorf("%s is declared twice", v.Name)
			}
			seen[v.Name] = true
		}
	})

	// Whatever a deployment takes from a Kubernetes Secret is a Secret: that
	// is the rule redaction rests on, so a credential declared as a plain
	// string is a bug.
	t.Run("credentials are secrets", func(t *testing.T) {
		for _, v := range vars {
			if looksLikeCredential(v.Name) && !v.Secret {
				t.Errorf("%s is a credential but not declared as a Secret", v.Name)
			}
		}
	})

	t.Run("secrets do not leak", func(t *testing.T) {
		checkSecretsDoNotLeak(t, newRoot, vars)
	})

	// The list of variables checked is the declarations, not a list somebody
	// has to remember to extend.
	t.Run("placeholders are refused", func(t *testing.T) {
		for _, v := range vars {
			value := config.PlaceholderMarker + "_" + strings.ToLower(v.Name)
			err := config.Load([]string{v.Name + "=" + value}, newRoot())
			if !errors.Is(err, config.ErrPlaceholder) || !strings.Contains(err.Error(), v.Name) {
				t.Errorf("%s: err = %v, want the placeholder refusal naming it", v.Name, err)
			}
			if err != nil && strings.Contains(err.Error(), value) {
				t.Errorf("%s: the error repeats the value", v.Name)
			}
		}
	})
}

// looksLikeCredential is the naming rule for what the manifests take from a
// Kubernetes Secret.
func looksLikeCredential(name string) bool {
	return strings.Contains(name, "SECRET") || strings.Contains(name, "PASSWORD") ||
		strings.HasSuffix(name, "_KEY") || strings.HasSuffix(name, "_KEY_OLD") ||
		strings.Contains(name, "ACCESS_KEY") || name == "OC_SERVICE_ACCOUNT_ID"
}

// checkSecretsDoNotLeak sets every Secret to a distinct random value and looks
// for those values wherever a configuration is likely to be printed.
func checkSecretsDoNotLeak(t *testing.T, newRoot func() any, vars []config.Variable) {
	var environ, secrets []string
	for _, v := range vars {
		if v.Secret {
			value := randomKey(t)
			environ = append(environ, v.Name+"="+value)
			secrets = append(secrets, value)
		}
	}
	root := newRoot()
	// The load may fail (a credential without what it belongs to); the
	// secrets are set either way, and the error must not carry them either.
	loadErr := config.Load(environ, root)

	var line bytes.Buffer
	slog.New(slog.NewJSONHandler(&line, nil)).Info("configuration", "config", config.Effective(root))
	outputs := map[string]string{"effective configuration": line.String()}
	if loadErr != nil {
		outputs["load error"] = loadErr.Error()
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		outputs["Sprintf "+format] = fmt.Sprintf(format, root)
	}
	if out, err := json.Marshal(root); err == nil {
		outputs["json"] = string(out)
	}
	for where, out := range outputs {
		for _, value := range secrets {
			if strings.Contains(out, value) {
				t.Errorf("a secret value reaches the %s", where)
			}
		}
	}

	var decoded struct{ Config map[string]any }
	if err := json.Unmarshal(line.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, v := range vars {
		got, ok := decoded.Config[strings.ToLower(v.Name)]
		if !ok {
			t.Errorf("%s is missing from the effective configuration", v.Name)
		}
		if v.Secret && got != "[redacted]" {
			t.Errorf("%s is set but shown as %v", v.Name, got)
		}
	}
}

// randomKey is a fresh base64 string of 32 random bytes: a valid wrapping
// key, so key-shape checks do not hide the leak checks behind other errors.
// Generated at runtime: a key-shaped literal in a test is indistinguishable
// from a leaked one.
func randomKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// CheckReference fails when the reference file at path is not what p
// generates. With -update it rewrites the file instead.
func CheckReference(t *testing.T, p config.Program, path string) {
	t.Helper()
	want := config.Reference(p)
	if *update {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run %s)", path, err, p.Generator)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale: run %s and commit the result", path, p.Generator)
	}
}
