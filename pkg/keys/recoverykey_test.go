package keys

import (
	"bytes"
	"strings"
	"testing"
)

func TestRecoveryKeyRoundTrip(t *testing.T) {
	display, secret, err := GenerateRecoveryKey()
	if err != nil {
		t.Fatalf("GenerateRecoveryKey: %v", err)
	}
	if !strings.HasPrefix(display, RKPrefix+"-") {
		t.Fatalf("missing versioned prefix: %q", display)
	}
	if len(secret) != rkEntropyBytes {
		t.Fatalf("secret length = %d, want %d", len(secret), rkEntropyBytes)
	}

	decoded, err := DecodeRecoveryKey(display)
	if err != nil {
		t.Fatalf("DecodeRecoveryKey: %v", err)
	}
	if !bytes.Equal(decoded, secret) {
		t.Fatal("decoded secret does not match generated secret")
	}
}

func TestRecoveryKeyIsTypable(t *testing.T) {
	display, _, err := GenerateRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimPrefix(display, RKPrefix)
	for _, r := range strings.ToUpper(strings.ReplaceAll(body, "-", "")) {
		if !strings.ContainsRune(crockford, r) {
			t.Fatalf("non-Crockford character %q in %q", r, display)
		}
		// Crockford excludes the ambiguous letters entirely.
		if strings.ContainsRune("ILOU", r) {
			t.Fatalf("ambiguous character %q must not appear", r)
		}
	}
	if !strings.Contains(display, "-") {
		t.Fatal("expected dash-grouped output for readability")
	}
}

func TestRecoveryKeyDecodeTolerance(t *testing.T) {
	display, secret, err := GenerateRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}

	variants := map[string]string{
		"as generated": display,
		"lower case":   strings.ToLower(display),
		"no dashes":    strings.ReplaceAll(display, "-", ""),
		"spaces":       strings.ReplaceAll(display, "-", " "),
		"surrounding":  "  " + display + "\n",
		"without prefix": strings.TrimPrefix(
			strings.TrimPrefix(display, RKPrefix), "-"),
	}
	for name, v := range variants {
		t.Run(name, func(t *testing.T) {
			got, err := DecodeRecoveryKey(v)
			if err != nil {
				t.Fatalf("decode %s: %v", name, err)
			}
			if !bytes.Equal(got, secret) {
				t.Fatalf("decode %s: secret mismatch", name)
			}
		})
	}
}

func TestRecoveryKeyLookalikeMapping(t *testing.T) {
	// A user transcribing by hand may type I/l for 1 and O for 0; Crockford
	// says those map back, so such a key must still decode.
	entropy := bytes.Repeat([]byte{0x01}, rkEntropyBytes)
	display := EncodeRecoveryKey(entropy)

	// Mangle only the payload; the version prefix is copied verbatim in practice.
	body := strings.TrimPrefix(display, RKPrefix)
	body = strings.ReplaceAll(body, "1", "I")
	body = strings.ReplaceAll(body, "0", "O")
	mangled := RKPrefix + body

	got, err := DecodeRecoveryKey(mangled)
	if err != nil {
		t.Fatalf("look-alike characters must decode: %v", err)
	}
	if !bytes.Equal(got, entropy) {
		t.Fatal("look-alike decode mismatch")
	}
}

func TestRecoveryKeyChecksumCatchesTypos(t *testing.T) {
	display, _, err := GenerateRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	// Change one payload character to something else in the alphabet.
	runes := []rune(display)
	idx := len(runes) - 1
	orig := runes[idx]
	for _, c := range crockford {
		if c != orig {
			runes[idx] = c
			break
		}
	}
	typo := string(runes)
	if typo == display {
		t.Skip("could not construct a typo")
	}
	if _, err := DecodeRecoveryKey(typo); err == nil {
		t.Fatal("checksum must catch a single-character typo")
	}
}

// The last character of a Recovery Key carries padding bits. A typo confined to
// them would decode to identical bytes, so it must be rejected outright rather
// than sail past the checksum.
func TestRecoveryKeyRejectsEveryLastCharacterTypo(t *testing.T) {
	display, _, err := GenerateRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}

	runes := []rune(display)
	idx := len(runes) - 1
	orig := runes[idx]

	for _, c := range crockford {
		if c == orig {
			continue
		}
		runes[idx] = c
		if _, err := DecodeRecoveryKey(string(runes)); err == nil {
			t.Fatalf("typo %q -> %q was accepted", string(orig), string(c))
		}
	}
}

func TestRecoveryKeyRejectsGarbage(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"illegal char":     "ocbk1-!!!!!-!!!!!",
		"too short":        "ocbk1-ABCDE",
		"wrong version":    "ocbk2-ABCDE-ABCDE-ABCDE-ABCDE-ABCDE-ABCDE-ABCDE",
		"random plausible": "ocbk1-ZZZZZ-ZZZZZ-ZZZZZ-ZZZZZ-ZZZZZ-ZZZZZ-ZZZZZ",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeRecoveryKey(in); err == nil {
				t.Fatalf("%s must be rejected", name)
			}
		})
	}
}

func TestRecoveryKeyErrorsLeakNoInput(t *testing.T) {
	// A mistyped key must never appear in an error string, or it could reach a
	// log (AGENTS.md: never log key material).
	secretish := "ocbk1-ABCDE-ABCDE-ABCDE-ABCDE-ABCDE-ABCDE-ABCDF"
	_, err := DecodeRecoveryKey(secretish)
	if err == nil {
		t.Skip("expected this key to be invalid")
	}
	if strings.Contains(err.Error(), "ABCDE") || strings.Contains(err.Error(), secretish) {
		t.Fatalf("error leaked key material: %v", err)
	}
}

func TestRecoveryKeyUniqueness(t *testing.T) {
	// Property: generated RKs do not collide.
	const n = 500
	seen := make(map[string]struct{}, n)
	for range n {
		display, _, err := GenerateRecoveryKey()
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[display]; dup {
			t.Fatal("duplicate recovery key generated")
		}
		seen[display] = struct{}{}
	}
}

func TestRecoveryKeyEncodingRoundTripsProperty(t *testing.T) {
	// Property: encode->decode is the identity for arbitrary entropy.
	for range 200 {
		_, secret, err := GenerateRecoveryKey()
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeRecoveryKey(EncodeRecoveryKey(secret))
		if err != nil {
			t.Fatalf("round-trip decode: %v", err)
		}
		if !bytes.Equal(got, secret) {
			t.Fatal("round-trip mismatch")
		}
	}
}

func TestRecoveryKeyWrapsDataKey(t *testing.T) {
	// End-to-end: a generated RK actually wraps and recovers a DK.
	_, secret, err := GenerateRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	dk := mustDK(t)

	env, err := WrapWithRK(dk, secret, testArgon)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnwrapRK(env, secret)
	if err != nil {
		t.Fatalf("unwrap with generated RK: %v", err)
	}
	if !bytes.Equal(got, dk) {
		t.Fatal("DK mismatch after RK round-trip")
	}
}
