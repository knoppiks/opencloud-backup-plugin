// Package publicapi holds the test that keeps the public packages self-contained.
//
// Everything under pkg/ is a reference implementation of a format that outlives
// this service (decisions.md #26): the key envelope, the Recovery Key, the
// Take-Out and its decryption. Code outside this module may import those
// packages, but it cannot import anything under internal/. An exported
// function, field or method that names an internal type is therefore unusable
// from outside, however public it looks. The test here fails when one does.
//
// The package has no code of its own.
package publicapi
