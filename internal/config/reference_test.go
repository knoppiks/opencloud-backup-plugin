package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The checked-in reference is what the code generates. When this fails, run
// go generate ./internal/config and commit the result.
func TestReferenceIsCurrent(t *testing.T) {
	path := filepath.Join("..", "..", filepath.FromSlash(ReferencePath))
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run go generate ./internal/config)", path, err)
	}
	if string(onDisk) != string(Reference()) {
		t.Fatalf("%s is stale: run go generate ./internal/config and commit the result", ReferencePath)
	}
}

func TestReferenceListsEveryVariable(t *testing.T) {
	ref := string(Reference())
	for _, p := range programs() {
		for _, v := range variables(reflect.ValueOf(p.cfg).Elem()) {
			if !strings.Contains(ref, "| `"+v.env+"` |") {
				t.Errorf("%s is not in the reference", v.env)
			}
			if v.doc == "" {
				t.Errorf("%s has no doc tag", v.env)
			}
		}
	}
}

// A secret's default would be a secret in the documentation.
func TestSecretsHaveNoDefault(t *testing.T) {
	for _, p := range programs() {
		for _, v := range variables(reflect.ValueOf(p.cfg).Elem()) {
			if v.typ == secretType && v.def != "" {
				t.Errorf("%s is a secret with a default", v.env)
			}
		}
	}
}

// Each variable belongs to one program's struct once; a duplicate name would
// be parsed twice and documented twice.
func TestVariableNamesAreUnique(t *testing.T) {
	for _, p := range programs() {
		seen := map[string]bool{}
		for _, v := range variables(reflect.ValueOf(p.cfg).Elem()) {
			if seen[v.env] {
				t.Errorf("%s: %s is declared twice", p.name, v.env)
			}
			seen[v.env] = true
		}
	}
}

func TestCodeNames(t *testing.T) {
	known := map[string]bool{"TLS_KEY_FILE": true}
	got := codeNames("Set with TLS_KEY_FILE, not TLS or TLS_KEY_FILES.", known)
	want := "Set with `TLS_KEY_FILE`, not TLS or TLS_KEY_FILES."
	if got != want {
		t.Fatalf("codeNames = %q, want %q", got, want)
	}
}

func TestLoadTakeout(t *testing.T) {
	c, err := LoadTakeout([]string{"S3_ACCESS_KEY_ID=id", "S3_SECRET_ACCESS_KEY=secret"})
	if err != nil {
		t.Fatal(err)
	}
	if c.S3.AccessKeyID.Reveal() != "id" || c.S3.SecretAccessKey.Reveal() != "secret" {
		t.Fatalf("Takeout = %+v", c)
	}
	if _, err := LoadTakeout([]string{"S3_ACCESS_KEY_ID=REPLACE_ME"}); err == nil {
		t.Fatal("a placeholder credential was accepted")
	}
	if c, err := LoadTakeout(nil); err != nil || c.S3.AccessKeyID.IsSet() {
		t.Fatalf("empty = %+v, %v", c, err)
	}
}
