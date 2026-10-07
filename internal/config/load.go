// Package config is the one place the binaries' environment is read, defaulted
// and validated (review-2026-10.md G4).
//
// Every variable is a field of a struct, declared with its name, its default,
// and a sentence for the operator:
//
//	Addr string `env:"BACKUPD_ADDR" default:":8080" doc:"..."`
//
// From those declarations the package parses the environment, refuses the
// unedited manifest's placeholders, logs the effective configuration with
// secrets redacted by type (Secret), and renders the environment reference
// the documentation publishes (Reference; regenerate with go generate).
//
// Supported field types: string, Secret, bool (exactly "true" or "false"),
// int (non-negative), and []string (comma-separated). A nested struct is a
// section; its `section` tag is the heading in the reference.
package config

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// PlaceholderMarker is what the shipped manifests put where a deployer must
// fill something in.
const PlaceholderMarker = "REPLACE_ME"

// ErrPlaceholder is returned when a variable still holds the manifest's
// placeholder.
var ErrPlaceholder = errors.New("placeholder value")

// variable is one environment variable, as declared on a struct field.
type variable struct {
	env     string
	def     string
	doc     string
	values  []string
	section string
	typ     reflect.Type
	value   reflect.Value
}

var (
	secretType      = reflect.TypeOf(Secret{})
	stringSliceType = reflect.TypeOf([]string(nil))
)

// variables lists the variables declared on v, a struct, in declaration order.
// Nested structs are sections; unexported fields are not configuration.
func variables(v reflect.Value) []variable {
	return collect(v, "")
}

func collect(v reflect.Value, section string) []variable {
	var out []variable
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Type.Kind() == reflect.Struct && f.Type != secretType {
			out = append(out, collect(v.Field(i), f.Tag.Get("section"))...)
			continue
		}
		env := f.Tag.Get("env")
		if env == "" {
			panic(fmt.Sprintf("config: field %s has no env tag", f.Name))
		}
		var values []string
		if raw := f.Tag.Get("values"); raw != "" {
			values = strings.Split(raw, ",")
		}
		out = append(out, variable{
			env:     env,
			def:     f.Tag.Get("default"),
			doc:     f.Tag.Get("doc"),
			values:  values,
			section: section,
			typ:     f.Type,
			value:   v.Field(i),
		})
	}
	return out
}

// environment indexes "NAME=value" entries. The first entry for a name wins,
// as it does for os.Getenv.
func environment(environ []string) map[string]string {
	env := make(map[string]string, len(environ))
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, seen := env[name]; !seen {
			env[name] = value
		}
	}
	return env
}

// parse fills dst, a pointer to a struct, from environ. Every malformed
// variable is reported, not just the first, so one restart shows them all.
func parse(environ []string, dst any) error {
	env := environment(environ)
	vars := variables(reflect.ValueOf(dst).Elem())

	if err := checkPlaceholders(env, vars); err != nil {
		return err
	}

	var errs []error
	for _, v := range vars {
		raw := env[v.env]
		if v.typ != secretType {
			raw = strings.TrimSpace(raw)
		}
		if raw == "" {
			raw = v.def
		}
		if err := set(v, raw); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// set parses raw into the variable's field. Error messages quote the value
// for every type except Secret, whose value never appears in one.
func set(v variable, raw string) error {
	switch {
	case v.typ == secretType:
		v.value.Set(reflect.ValueOf(NewSecret(raw)))
	case v.typ == stringSliceType:
		v.value.Set(reflect.ValueOf(splitList(raw)))
	case v.typ.Kind() == reflect.String:
		if raw != "" && len(v.values) > 0 {
			canonical, ok := oneOf(v.values, raw)
			if !ok {
				return fmt.Errorf("%s must be one of %s, got %q", v.env, strings.Join(v.values, ", "), raw)
			}
			raw = canonical
		}
		v.value.SetString(raw)
	case v.typ.Kind() == reflect.Bool:
		b, err := parseBool(raw)
		if err != nil {
			return fmt.Errorf("%s must be true or false, got %q", v.env, raw)
		}
		v.value.SetBool(b)
	case v.typ.Kind() == reflect.Int:
		n, err := parseCount(raw)
		if err != nil {
			return fmt.Errorf("%s must be a non-negative integer, got %q", v.env, raw)
		}
		v.value.SetInt(int64(n))
	default:
		panic(fmt.Sprintf("config: %s has unsupported type %s", v.env, v.typ))
	}
	return nil
}

// parseBool accepts exactly "true" and "false"; empty is false. Anything else
// is a typo, and a typo in a switch must not quietly mean "off".
func parseBool(raw string) (bool, error) {
	switch raw {
	case "true":
		return true, nil
	case "false", "":
		return false, nil
	default:
		return false, errors.New("not a boolean")
	}
}

// parseCount accepts a non-negative integer; empty is zero.
func parseCount(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, errors.New("not a non-negative integer")
	}
	return n, nil
}

// splitList splits a comma-separated list, dropping empty items.
func splitList(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// oneOf returns the allowed value s names, ignoring case: these are keywords,
// and "Memory" means the same as "memory".
func oneOf(allowed []string, s string) (string, bool) {
	for _, item := range allowed {
		if strings.EqualFold(item, s) {
			return item, true
		}
	}
	return "", false
}

// checkPlaceholders refuses a manifest that was deployed unedited.
//
// The placeholders are not all equal: an unreplaced wrapping key fails loudly
// on its own (it is not base64), while an unreplaced OIDC audience or state
// Space id used to start perfectly well and then reject every token, or write
// state to a Space that does not exist. "Comes up and does not work" is the
// worst of the available outcomes, because it looks like a bug in the service
// rather than an unfinished deployment.
//
// Only the variables declared here are checked: a placeholder belonging to
// some other component of the deployment is not this program's to refuse.
func checkPlaceholders(env map[string]string, vars []variable) error {
	var names []string
	for _, v := range vars {
		if strings.Contains(env[v.env], PlaceholderMarker) {
			names = append(names, v.env)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%w: %s still holds the manifest's placeholder value: fill it in before deploying "+
			"(see deploy/ and the README preconditions)", ErrPlaceholder, strings.Join(names, ", "))
}
