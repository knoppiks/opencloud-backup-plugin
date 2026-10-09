package config

import (
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
)

// Variable describes one declared environment variable, for documentation
// and for tests that check a binary's declarations as a whole.
type Variable struct {
	// Name is the environment variable.
	Name string
	// Section is the heading the variable is listed under.
	Section string
	// Type is what the variable accepts, in words ("boolean", "secret", ...).
	Type string
	// Default is the value an unset variable has, as it would be written in
	// the environment; "" when there is none.
	Default string
	// Doc is the operator-facing description.
	Doc string
	// Secret reports whether the variable is declared as a Secret.
	Secret bool
}

// Describe lists the variables declared on root, a pointer to a struct, in
// declaration order. Defaults are the values root holds.
func Describe(root any) []Variable {
	vars := variables(structOf(root))
	out := make([]Variable, len(vars))
	for i, v := range vars {
		out[i] = Variable{
			Name:    v.env,
			Section: v.section,
			Type:    typeName(v),
			Default: defaultOf(v),
			Doc:     v.doc,
			Secret:  v.typ == secretType,
		}
	}
	return out
}

// Names lists the variables declared on root, in declaration order.
func Names(root any) []string {
	vars := variables(structOf(root))
	names := make([]string, len(vars))
	for i, v := range vars {
		names[i] = v.env
	}
	return names
}

// Effective is the configuration root holds, one attribute per variable,
// keyed by the variable's name in lower case, unset ones included so defaults
// are visible. Secrets show only whether they are set: the Secret type
// redacts itself, so this needs no list of names.
func Effective(root any) slog.Value {
	vars := variables(structOf(root))
	attrs := make([]slog.Attr, 0, len(vars))
	for _, v := range vars {
		attrs = append(attrs, slog.Any(strings.ToLower(v.env), v.value.Interface()))
	}
	return slog.GroupValue(attrs...)
}

// typeName describes what a variable accepts.
func typeName(v variable) string {
	switch {
	case v.typ == secretType:
		return "secret"
	case v.typ == stringSliceType:
		return "comma-separated list"
	case len(v.values) > 0:
		return "one of `" + strings.Join(v.values, "`, `") + "`"
	case v.typ.Kind() == reflect.Bool:
		return "boolean"
	case v.typ.Kind() == reflect.Int:
		return "integer"
	default:
		return "string"
	}
}

// defaultOf renders the value a variable holds before Load, as it would be
// written in the environment. A Secret never has one: a secret's default
// would be a secret in the documentation.
func defaultOf(v variable) string {
	switch {
	case v.typ == secretType:
		if v.value.Interface().(Secret).IsSet() {
			panic(fmt.Sprintf("config: %s is a secret with a default", v.env))
		}
		return ""
	case v.typ == stringSliceType:
		return strings.Join(v.value.Interface().([]string), ",")
	case v.typ.Kind() == reflect.Bool:
		return strconv.FormatBool(v.value.Bool())
	case v.typ.Kind() == reflect.Int:
		return strconv.FormatInt(v.value.Int(), 10)
	default:
		return v.value.String()
	}
}
