package targets

import (
	"bytes"
	"strings"
	"testing"

	"opencloud-backup-plugin/pkg/keys"
)

func mustTWKey(t *testing.T) []byte {
	t.Helper()
	k, err := keys.GenerateSRWKey()
	if err != nil {
		t.Fatalf("generate TW key: %v", err)
	}
	return k
}

func mustSealer(t *testing.T, key []byte) CredSealer {
	t.Helper()
	s, err := NewCredSealer(key)
	if err != nil {
		t.Fatalf("NewCredSealer: %v", err)
	}
	return s
}

func TestCredSealRoundTrip(t *testing.T) {
	sealer := mustSealer(t, mustTWKey(t))
	in := PlainCreds{AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "s3cr3t-value"}

	blob, version, err := sealer.Seal(in)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if version != keys.EnvelopeVersion {
		t.Fatalf("version = %d, want %d", version, keys.EnvelopeVersion)
	}

	// The stored blob must not contain the plaintext credentials.
	if bytes.Contains(blob, []byte(in.SecretAccessKey)) {
		t.Fatal("secret access key found in sealed blob")
	}
	if bytes.Contains(blob, []byte(in.AccessKeyID)) {
		t.Fatal("access key id found in sealed blob")
	}

	got, err := sealer.Open(blob)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != in {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestCredSealWrongKeyFails(t *testing.T) {
	blob, _, err := mustSealer(t, mustTWKey(t)).Seal(
		PlainCreds{AccessKeyID: "a", SecretAccessKey: "b"})
	if err != nil {
		t.Fatal(err)
	}
	other := mustSealer(t, mustTWKey(t))
	got, err := other.Open(blob)
	if err == nil {
		t.Fatal("a different TW key must not open the blob")
	}
	if got.AccessKeyID != "" || got.SecretAccessKey != "" {
		t.Fatal("failed Open must not return credentials")
	}
}

func TestCredSealTamperedBlobFails(t *testing.T) {
	sealer := mustSealer(t, mustTWKey(t))
	blob, _, err := sealer.Seal(PlainCreds{AccessKeyID: "a", SecretAccessKey: "b"})
	if err != nil {
		t.Fatal(err)
	}
	blob[len(blob)-1] ^= 0x01
	if _, err := sealer.Open(blob); err == nil {
		t.Fatal("tampered credential blob must fail authentication")
	}
}

func TestCredSealErrorsLeakNothing(t *testing.T) {
	sealer := mustSealer(t, mustTWKey(t))
	secret := "super-secret-key-value"
	blob, _, err := sealer.Seal(PlainCreds{AccessKeyID: "AKIA", SecretAccessKey: secret})
	if err != nil {
		t.Fatal(err)
	}
	blob[len(blob)-1] ^= 0xff

	_, err = sealer.Open(blob)
	if err == nil {
		t.Fatal("expected failure")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) || strings.Contains(msg, "AKIA") {
		t.Fatalf("error leaked credential material: %v", err)
	}
	// Must not distinguish wrong-key from tampered.
	if strings.Contains(strings.ToLower(msg), "tamper") || strings.Contains(strings.ToLower(msg), "wrong key") {
		t.Fatalf("error discloses failure mode: %v", err)
	}
}

func TestCredSealRejectsBadInput(t *testing.T) {
	if _, err := NewCredSealer([]byte("too short")); err == nil {
		t.Fatal("short TW key must be rejected")
	}
	sealer := mustSealer(t, mustTWKey(t))
	if _, _, err := sealer.Seal(PlainCreds{}); err == nil {
		t.Fatal("empty credentials must be rejected")
	}
	if _, _, err := sealer.Seal(PlainCreds{AccessKeyID: "a"}); err == nil {
		t.Fatal("incomplete credentials must be rejected")
	}
	if _, err := sealer.Open(nil); err == nil {
		t.Fatal("empty blob must be rejected")
	}
}

func TestCredSealRejectsForeignEnvelopeKind(t *testing.T) {
	// A DK envelope must not be openable as a credential blob, even with the
	// same key bytes — the envelope binds its kind.
	key := mustTWKey(t)
	dk, err := keys.GenerateDK()
	if err != nil {
		t.Fatal(err)
	}
	dkEnv, err := keys.WrapWithSRW(dk, key)
	if err != nil {
		t.Fatal(err)
	}
	sealer := mustSealer(t, key)
	if _, err := sealer.Open(dkEnv.Blob); err == nil {
		t.Fatal("an SRW (DK) envelope must not open as credentials")
	}
}

func TestCredSealNonDeterministic(t *testing.T) {
	sealer := mustSealer(t, mustTWKey(t))
	in := PlainCreds{AccessKeyID: "a", SecretAccessKey: "b"}
	b1, _, _ := sealer.Seal(in)
	b2, _, _ := sealer.Seal(in)
	if bytes.Equal(b1, b2) {
		t.Fatal("sealed blobs must not be deterministic")
	}
}

func TestStoredTargetNeverExposesCredsThroughPublicView(t *testing.T) {
	// Integration of the sealer with the store: a target carries only the sealed
	// blob, and the user-facing projection carries neither.
	sealer := mustSealer(t, mustTWKey(t))
	blob, version, err := sealer.Seal(PlainCreds{AccessKeyID: "AKIA", SecretAccessKey: "shh"})
	if err != nil {
		t.Fatal(err)
	}
	tgt := Target{ID: "t1", Name: "Buddy", WrappedCreds: blob, Version: version}

	pv := tgt.Public()
	if pv.ID != "t1" || pv.Name != "Buddy" {
		t.Fatalf("unexpected public view: %+v", pv)
	}
	if bytes.Contains([]byte(pv.ID+pv.Name), []byte("shh")) {
		t.Fatal("public view leaked credentials")
	}
}
