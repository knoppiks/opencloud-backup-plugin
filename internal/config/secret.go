package config

import "log/slog"

// redacted is what a set Secret prints as, wherever it is printed.
const redacted = "[redacted]"

// Secret is a configuration value that must never be printed: a wrapping key,
// a password, a credential.
//
// Redaction is a property of the type, not of a list of variable names: a
// field declared as Secret cannot reach a log line, an error message or a
// "%v" of the configuration, whatever its name and whoever adds it later.
// The value sits behind an unexported pointer so even the formatting paths
// that bypass String (a "%d", or reflection over unexported fields) see an
// address, never the text.
type Secret struct {
	value *string
}

// NewSecret wraps a value. An empty value is an unset Secret.
func NewSecret(value string) Secret {
	if value == "" {
		return Secret{}
	}
	return Secret{value: &value}
}

// Reveal returns the value. Call it only where the value is used, never to
// print it.
func (s Secret) Reveal() string {
	if s.value == nil {
		return ""
	}
	return *s.value
}

// IsSet reports whether a value was given.
func (s Secret) IsSet() bool { return s.value != nil }

// String says whether the Secret is set, and nothing else.
func (s Secret) String() string {
	if s.IsSet() {
		return redacted
	}
	return ""
}

// GoString covers "%#v".
func (s Secret) GoString() string { return "config.Secret(" + s.String() + ")" }

// LogValue keeps the value out of structured logs.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// MarshalText covers every encoder that honours encoding.TextMarshaler,
// encoding/json among them.
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.String()), nil }
