// Package config is the machinery the binaries read their environment with
// (review-2026-10.md G4). It knows nothing about what is configured: each
// component declares the variables it needs on a struct of its own, and a
// binary composes those structs into one root and hands it to Load.
//
// A variable is a tagged field:
//
//	Addr string `env:"BACKUPD_ADDR" doc:"..."`
//
// Its default is whatever the field holds when Load is called, so a component
// declares its defaults in code, from its own constants, by providing a
// constructor for its struct. A nested struct is a section; its `section` tag
// is the heading in the reference. A section (or the root) that implements
// Validator is checked after parsing, and may normalise what it checks.
//
// From the declarations the package parses the environment, refuses the
// unedited manifest's placeholders, renders the effective configuration with
// secrets redacted by type (Secret), and renders the environment reference
// the documentation publishes (Reference).
//
// Supported field types: string, Secret, bool (exactly "true" or "false"),
// int (non-negative), and []string (comma-separated). A string field may
// restrict its values with a `values:"a,b"` tag (matched ignoring case).
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

// Validator is implemented by a section that checks what depends on more than
// one of its variables, or on a variable's meaning rather than its type. It is
// called on a pointer, so it may normalise the values it checks.
type Validator interface {
	Validate() error
}

// variable is one environment variable, as declared on a struct field.
type variable struct {
	env     string
	doc     string
	values  []string
	section string
	typ     reflect.Type
	value   reflect.Value
}

var (
	secretType      = reflect.TypeOf(Secret{})
	stringSliceType = reflect.TypeOf([]string(nil))
	validatorType   = reflect.TypeOf((*Validator)(nil)).Elem()
)

// structOf returns the struct root points to, refusing anything else: a
// non-pointer would be parsed into a copy and silently lost.
func structOf(root any) reflect.Value {
	v := reflect.ValueOf(root)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		panic(fmt.Sprintf("config: want a pointer to a struct, got %T", root))
	}
	return v.Elem()
}

// isSection reports whether a field of type t is a nested section rather than
// a variable.
func isSection(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && t != secretType
}

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
		if isSection(f.Type) {
			sub := f.Tag.Get("section")
			if sub == "" {
				sub = section
			}
			out = append(out, collect(v.Field(i), sub)...)
			continue
		}
		env := f.Tag.Get("env")
		if env == "" {
			panic(fmt.Sprintf("config: field %s.%s has no env tag", t.Name(), f.Name))
		}
		var values []string
		if raw := f.Tag.Get("values"); raw != "" {
			values = strings.Split(raw, ",")
		}
		out = append(out, variable{
			env:     env,
			doc:     f.Tag.Get("doc"),
			values:  values,
			section: section,
			typ:     f.Type,
			value:   v.Field(i),
		})
	}
	return out
}

// Load fills root, a pointer to a struct, from environ ("NAME=value" entries,
// as os.Environ returns them) and validates it. Fields keep the value they
// hold for a variable that is unset or empty: that value is the default.
//
// Every problem is reported in the one error, so one restart shows them all.
// The exception is an unreplaced placeholder, which is reported alone: every
// diagnosis after an unfinished manifest is of a symptom.
func Load(environ []string, root any) error {
	v := structOf(root)
	env := environment(environ)
	vars := variables(v)

	if err := checkPlaceholders(env, vars); err != nil {
		return err
	}

	var errs []error
	for _, variable := range vars {
		raw, ok := env[variable.env]
		if variable.typ != secretType {
			raw = strings.TrimSpace(raw)
		}
		if !ok || raw == "" {
			continue
		}
		if err := set(variable, raw); err != nil {
			errs = append(errs, err)
		}
	}
	// Validated even when a variable did not parse, so one restart shows
	// every problem; a variable that did not parse keeps its default.
	errs = append(errs, validate(v))
	return errors.Join(errs...)
}

// validate runs every Validator in v, sections before the struct that holds
// them, so a check that spans sections sees them normalised.
func validate(v reflect.Value) error {
	var errs []error
	t := v.Type()
	for i := range t.NumField() {
		if f := t.Field(i); f.IsExported() && isSection(f.Type) {
			errs = append(errs, validate(v.Field(i)))
		}
	}
	if v.Addr().Type().Implements(validatorType) {
		errs = append(errs, v.Addr().Interface().(Validator).Validate())
	}
	return errors.Join(errs...)
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

// set parses raw into the variable's field. Error messages quote the value
// for every type except Secret, whose value never appears in one.
func set(v variable, raw string) error {
	switch {
	case v.typ == secretType:
		v.value.Set(reflect.ValueOf(NewSecret(raw)))
	case v.typ == stringSliceType:
		v.value.Set(reflect.ValueOf(splitList(raw)))
	case v.typ.Kind() == reflect.String:
		if len(v.values) > 0 {
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

// parseBool accepts exactly "true" and "false". Anything else is a typo, and
// a typo in a switch must not quietly mean "off".
func parseBool(raw string) (bool, error) {
	switch raw {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New("not a boolean")
	}
}

// parseCount accepts a non-negative integer.
func parseCount(raw string) (int, error) {
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
