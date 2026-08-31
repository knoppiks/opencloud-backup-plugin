package keys

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoLoggingInKeysPackage enforces the AGENTS.md rule "never log key
// material" structurally: the key service must contain no logging calls at all,
// so no future edit can accidentally log a DK, RK, or wrapped blob.
func TestNoLoggingInKeysPackage(t *testing.T) {
	forbidden := regexp.MustCompile(`\b(log\.[A-Z]|slog\.[A-Z]|fmt\.Print|println\()`)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if loc := forbidden.FindIndex(src); loc != nil {
			line := 1 + bytes.Count(src[:loc[0]], []byte("\n"))
			t.Errorf("%s:%d: logging call in the key service — key material must never be logged", f, line)
		}
	}
}

// TestErrorsNeverFormatSecrets guards that error construction in the key service
// never interpolates a value with a string/bytes verb. Only %w (wrapped
// sentinels) and %d (sizes, versions) are allowed, so an error can never carry
// key bytes into a log.
func TestErrorsNeverFormatSecrets(t *testing.T) {
	// Matches a format verb that could render arbitrary data.
	risky := regexp.MustCompile(`(Errorf|Sprintf)\([^)]*%[svqxX]`)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, loc := range risky.FindAllIndex(src, -1) {
			line := 1 + bytes.Count(src[:loc[0]], []byte("\n"))
			t.Errorf("%s:%d: error formats a value with %%s/%%v/%%q/%%x — "+
				"use %%w or %%d so key material cannot leak: %q",
				f, line, string(src[loc[0]:loc[1]]))
		}
	}
}

// TestErrorsFromRealFailuresCarryNoSecrets is the behavioural counterpart: drive
// the actual failure paths and assert the resulting messages contain no part of
// the secrets involved.
func TestErrorsFromRealFailuresCarryNoSecrets(t *testing.T) {
	dk := mustDK(t)
	srw := mustSRWKey(t)
	rk := []byte("a-very-recognisable-recovery-key")

	rkEnv, err := WrapWithRK(dk, rk, testArgon)
	if err != nil {
		t.Fatal(err)
	}
	srwEnv, err := WrapWithSRW(dk, srw)
	if err != nil {
		t.Fatal(err)
	}

	tampered := make([]byte, len(srwEnv.Blob))
	copy(tampered, srwEnv.Blob)
	tampered[len(tampered)-1] ^= 0xff

	var errs []error
	collect := func(_ []byte, err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	collect(UnwrapRK(rkEnv, []byte("wrong-key")))
	collect(UnwrapSRW(srwEnv, mustSRWKey(t)))
	collect(UnwrapSRW(WrappedDK{Kind: WrapSRW, Blob: tampered}, srw))
	collect(UnwrapRK(srwEnv, rk))
	collect(UnwrapSRW(WrappedDK{Kind: WrapSRW, Blob: []byte("garbage")}, srw))

	if len(errs) == 0 {
		t.Fatal("expected failures to assert on")
	}
	secrets := [][]byte{dk, srw, rk, rkEnv.Blob, srwEnv.Blob}
	for _, err := range errs {
		msg := []byte(err.Error())
		for _, s := range secrets {
			if len(s) >= 8 && bytes.Contains(msg, s[:8]) {
				t.Fatalf("error leaked secret material: %v", err)
			}
		}
	}
}
