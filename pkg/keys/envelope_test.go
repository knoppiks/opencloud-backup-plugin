package keys

import (
	"bytes"
	"crypto/rand"
	"testing"
	"time"
)

// testArgon keeps unit tests fast; production uses DefaultArgonParams. The
// envelope records whatever parameters were used, so this does not weaken what
// the tests prove about the format.
var testArgon = ArgonParams{Time: 1, MemoryKiB: 8 * 1024, Lanes: 1, SaltLen: 16}

func mustDK(t *testing.T) []byte {
	t.Helper()
	dk, err := GenerateDK()
	if err != nil {
		t.Fatalf("GenerateDK: %v", err)
	}
	if len(dk) != DKSize {
		t.Fatalf("DK length = %d, want %d", len(dk), DKSize)
	}
	return dk
}

func mustSRWKey(t *testing.T) []byte {
	t.Helper()
	k, err := GenerateSRWKey()
	if err != nil {
		t.Fatalf("GenerateSRWKey: %v", err)
	}
	return k
}

func TestRKRoundTrip(t *testing.T) {
	dk := mustDK(t)
	rk := []byte("test-recovery-key-material")

	w, err := WrapWithRK(dk, rk, testArgon)
	if err != nil {
		t.Fatalf("WrapWithRK: %v", err)
	}
	if w.Kind != WrapRK || w.Version != EnvelopeVersion {
		t.Fatalf("unexpected envelope metadata: %+v", w)
	}
	// The wrapped blob must not contain the plaintext DK.
	if bytes.Contains(w.Blob, dk) {
		t.Fatal("plaintext DK found inside RK envelope")
	}

	got, err := UnwrapRK(w, rk)
	if err != nil {
		t.Fatalf("UnwrapRK: %v", err)
	}
	if !bytes.Equal(got, dk) {
		t.Fatal("round-tripped DK does not match")
	}
}

func TestSRWRoundTrip(t *testing.T) {
	dk := mustDK(t)
	srw := mustSRWKey(t)

	w, err := WrapWithSRW(dk, srw)
	if err != nil {
		t.Fatalf("WrapWithSRW: %v", err)
	}
	if w.Kind != WrapSRW {
		t.Fatalf("unexpected kind %v", w.Kind)
	}
	if bytes.Contains(w.Blob, dk) {
		t.Fatal("plaintext DK found inside SRW envelope")
	}

	got, err := UnwrapSRW(w, srw)
	if err != nil {
		t.Fatalf("UnwrapSRW: %v", err)
	}
	if !bytes.Equal(got, dk) {
		t.Fatal("round-tripped DK does not match")
	}
}

func TestWrongRKFailsCleanly(t *testing.T) {
	dk := mustDK(t)
	w, err := WrapWithRK(dk, []byte("right-key"), testArgon)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnwrapRK(w, []byte("wrong-key"))
	if err == nil {
		t.Fatal("wrong RK must fail")
	}
	if got != nil {
		t.Fatal("failed unwrap must not return plaintext")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("cannot unwrap")) {
		t.Fatalf("unexpected error text: %v", err)
	}
}

func TestWrongSRWKeyFails(t *testing.T) {
	dk := mustDK(t)
	w, _ := WrapWithSRW(dk, mustSRWKey(t))
	if _, err := UnwrapSRW(w, mustSRWKey(t)); err == nil {
		t.Fatal("wrong SRW key must fail")
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	dk := mustDK(t)
	srw := mustSRWKey(t)
	w, _ := WrapWithSRW(dk, srw)

	for _, tc := range []struct {
		name string
		mut  func(b []byte)
	}{
		{"flip last ciphertext byte", func(b []byte) { b[len(b)-1] ^= 0x01 }},
		{"flip first ciphertext byte", func(b []byte) { b[len(b)-20] ^= 0x80 }},
		{"flip kind in header", func(b []byte) { b[6] ^= 0x01 }},
		{"flip version in header", func(b []byte) { b[5] = 1 }},
		{"flip nonce byte", func(b []byte) { b[envelopeHeaderMin] ^= 0xff }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := make([]byte, len(w.Blob))
			copy(bad, w.Blob)
			tc.mut(bad)
			if bytes.Equal(bad, w.Blob) {
				t.Skip("mutation was a no-op")
			}
			tampered := WrappedDK{Version: w.Version, Kind: WrapSRW, Blob: bad}
			if _, err := UnwrapSRW(tampered, srw); err == nil {
				t.Fatal("tampered envelope must fail authentication")
			}
		})
	}
}

func TestTamperedArgonParamsFails(t *testing.T) {
	// The header is AEAD additional data, so rewriting the recorded Argon2id
	// cost must fail — either rejected as out of range, or caught by
	// authentication. Neither path may hang or succeed.
	dk := mustDK(t)
	rk := []byte("recovery")
	w, _ := WrapWithRK(dk, rk, testArgon)

	// Low-order bit: stays within the allowed range, so this must be caught by
	// AEAD authentication (the header is additional data).
	t.Run("in-range tamper fails authentication", func(t *testing.T) {
		bad := make([]byte, len(w.Blob))
		copy(bad, w.Blob)
		bad[11] ^= 0x01 // low byte of argonTime
		if _, err := UnwrapRK(WrappedDK{Kind: WrapRK, Blob: bad}, rk); err == nil {
			t.Fatal("tampered Argon2id parameters must fail authentication")
		}
	})

	// High-order bit: an absurd cost. This must be rejected by the range check
	// *before* derivation — otherwise it is a denial-of-service vector.
	t.Run("absurd cost rejected quickly", func(t *testing.T) {
		bad := make([]byte, len(w.Blob))
		copy(bad, w.Blob)
		bad[8] ^= 0x01 // high byte of argonTime -> ~16.7M passes

		done := make(chan error, 1)
		go func() {
			_, err := UnwrapRK(WrappedDK{Kind: WrapRK, Blob: bad}, rk)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("absurd Argon2id cost must be rejected")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("absurd Argon2id cost was not rejected before derivation (DoS)")
		}
	})
}

func TestArgonParamBoundsEnforced(t *testing.T) {
	for name, p := range map[string]ArgonParams{
		"zero time":    {Time: 0, MemoryKiB: 8192, Lanes: 1, SaltLen: 16},
		"zero memory":  {Time: 1, MemoryKiB: 0, Lanes: 1, SaltLen: 16},
		"zero lanes":   {Time: 1, MemoryKiB: 8192, Lanes: 0, SaltLen: 16},
		"time too big": {Time: maxArgonTime + 1, MemoryKiB: 8192, Lanes: 1, SaltLen: 16},
		"mem too big":  {Time: 1, MemoryKiB: maxArgonMemoryKiB + 1, Lanes: 1, SaltLen: 16},
		"lanes to big": {Time: 1, MemoryKiB: 8192, Lanes: maxArgonLanes + 1, SaltLen: 16},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateArgonParams(p); err == nil {
				t.Fatalf("params %+v must be rejected", p)
			}
		})
	}
	if err := validateArgonParams(DefaultArgonParams); err != nil {
		t.Fatalf("DefaultArgonParams must be valid: %v", err)
	}
}

func TestArgonParameterVersioning(t *testing.T) {
	// An envelope written with old parameters must still unwrap after the
	// defaults are raised, because parameters travel inside the envelope.
	dk := mustDK(t)
	rk := []byte("recovery")

	oldParams := ArgonParams{Time: 1, MemoryKiB: 8 * 1024, Lanes: 1, SaltLen: 16}
	newParams := ArgonParams{Time: 2, MemoryKiB: 16 * 1024, Lanes: 2, SaltLen: 16}

	oldEnv, err := WrapWithRK(dk, rk, oldParams)
	if err != nil {
		t.Fatal(err)
	}
	newEnv, err := WrapWithRK(dk, rk, newParams)
	if err != nil {
		t.Fatal(err)
	}

	// Both unwrap with the same RK, with no external parameter bookkeeping.
	for name, env := range map[string]WrappedDK{"old": oldEnv, "new": newEnv} {
		got, err := UnwrapRK(env, rk)
		if err != nil {
			t.Fatalf("%s params envelope failed to unwrap: %v", name, err)
		}
		if !bytes.Equal(got, dk) {
			t.Fatalf("%s params envelope round-trip mismatch", name)
		}
	}

	// And the recorded parameters are what we asked for.
	info, err := Inspect(oldEnv.Blob)
	if err != nil {
		t.Fatal(err)
	}
	if info.Argon.Time != oldParams.Time || info.Argon.MemoryKiB != oldParams.MemoryKiB {
		t.Fatalf("envelope did not record its own parameters: %+v", info.Argon)
	}
}

func TestRotateSRWKeepsDataKey(t *testing.T) {
	dk := mustDK(t)
	oldKey := mustSRWKey(t)
	newKey := mustSRWKey(t)

	oldEnv, _ := WrapWithSRW(dk, oldKey)
	newEnv, err := RotateSRW(oldEnv, oldKey, newKey)
	if err != nil {
		t.Fatalf("RotateSRW: %v", err)
	}

	// DK is unchanged -> existing snapshots stay readable.
	got, err := UnwrapSRW(newEnv, newKey)
	if err != nil {
		t.Fatalf("unwrap rotated envelope: %v", err)
	}
	if !bytes.Equal(got, dk) {
		t.Fatal("rotation must not change the DK")
	}
	// Old key no longer opens the new envelope.
	if _, err := UnwrapSRW(newEnv, oldKey); err == nil {
		t.Fatal("old SRW key must not open the rotated envelope")
	}
	// The new envelope is a distinct blob.
	if bytes.Equal(oldEnv.Blob, newEnv.Blob) {
		t.Fatal("rotation must produce a fresh envelope")
	}
}

func TestRotateRKKeepsDataKey(t *testing.T) {
	dk := mustDK(t)
	oldRK := []byte("old-recovery-key")
	newRK := []byte("new-recovery-key")

	oldEnv, _ := WrapWithRK(dk, oldRK, testArgon)
	newEnv, err := RotateRK(oldEnv, oldRK, newRK, testArgon)
	if err != nil {
		t.Fatalf("RotateRK: %v", err)
	}
	got, err := UnwrapRK(newEnv, newRK)
	if err != nil {
		t.Fatalf("unwrap rotated RK envelope: %v", err)
	}
	if !bytes.Equal(got, dk) {
		t.Fatal("RK rotation must not change the DK")
	}
	if _, err := UnwrapRK(newEnv, oldRK); err == nil {
		t.Fatal("old RK must not open the rotated envelope")
	}
}

func TestWrapKindMismatchRejected(t *testing.T) {
	dk := mustDK(t)
	srw := mustSRWKey(t)
	srwEnv, _ := WrapWithSRW(dk, srw)
	rkEnv, _ := WrapWithRK(dk, []byte("rk"), testArgon)

	if _, err := UnwrapRK(srwEnv, []byte("rk")); err == nil {
		t.Fatal("UnwrapRK must reject an SRW envelope")
	}
	if _, err := UnwrapSRW(rkEnv, srw); err == nil {
		t.Fatal("UnwrapSRW must reject an RK envelope")
	}
}

func TestMalformedEnvelopes(t *testing.T) {
	srw := mustSRWKey(t)
	cases := map[string][]byte{
		"empty":      {},
		"short":      []byte("OCBK"),
		"bad magic":  append([]byte("XXXXX"), make([]byte, 40)...),
		"truncated":  append([]byte(envelopeMagic), 1, byte(WrapSRW), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
		"bad kdf id": buildBadKDF(),
	}
	for name, blob := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := UnwrapSRW(WrappedDK{Kind: WrapSRW, Blob: blob}, srw); err == nil {
				t.Fatal("malformed envelope must be rejected")
			}
			if _, err := Inspect(blob); err == nil {
				t.Fatal("Inspect must reject malformed envelope")
			}
		})
	}
}

func buildBadKDF() []byte {
	h := buildHeader(EnvelopeVersion, WrapSRW, kdfID(99), ArgonParams{}, nil)
	return append(h, make([]byte, nonceSize+32)...)
}

func TestUnsupportedFutureVersionRejected(t *testing.T) {
	// A future-version envelope must be refused rather than misparsed — the
	// compatibility promise runs backwards, not forwards.
	blob := buildHeader(EnvelopeVersion+1, WrapSRW, kdfNone, ArgonParams{}, nil)
	blob = append(blob, make([]byte, nonceSize+32)...)
	if _, err := Inspect(blob); err == nil {
		t.Fatal("future envelope version must be rejected")
	}
}

func TestInspectRevealsNoSecrets(t *testing.T) {
	dk := mustDK(t)
	rk := []byte("recovery-key-abc")
	w, _ := WrapWithRK(dk, rk, testArgon)

	info, err := Inspect(w.Blob)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != WrapRK || info.KDF != "argon2id" {
		t.Fatalf("unexpected info: %+v", info)
	}
	// Inspect needs no key and must not expose one; the struct has no secret
	// fields by type, which the compiler enforces.
}

func TestZeroize(t *testing.T) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	Zeroize(b)
	for i, v := range b {
		if v != 0 {
			t.Fatalf("byte %d not zeroized", i)
		}
	}
}

func TestGenerateDKUniqueness(t *testing.T) {
	// Property: generated DKs do not collide.
	const n = 500
	seen := make(map[string]struct{}, n)
	for range n {
		dk := mustDK(t)
		k := string(dk)
		if _, dup := seen[k]; dup {
			t.Fatal("duplicate DK generated")
		}
		seen[k] = struct{}{}
	}
}

func TestEnvelopesAreNonDeterministic(t *testing.T) {
	// Same DK + same key must still produce different blobs (fresh nonce/salt).
	dk := mustDK(t)
	srw := mustSRWKey(t)
	a, _ := WrapWithSRW(dk, srw)
	b, _ := WrapWithSRW(dk, srw)
	if bytes.Equal(a.Blob, b.Blob) {
		t.Fatal("envelopes must not be deterministic")
	}
}

func TestInputValidation(t *testing.T) {
	dk := mustDK(t)
	short := []byte("too-short")

	if _, err := WrapWithSRW(dk, short); err == nil {
		t.Fatal("short SRW key must be rejected")
	}
	if _, err := WrapWithSRW(short, mustSRWKey(t)); err == nil {
		t.Fatal("wrong-size DK must be rejected")
	}
	if _, err := WrapWithRK(short, []byte("rk"), testArgon); err == nil {
		t.Fatal("wrong-size DK must be rejected for RK wrap")
	}
	if _, err := WrapWithRK(dk, nil, testArgon); err == nil {
		t.Fatal("empty RK must be rejected")
	}
	if _, err := NewSRWWrapper(short); err == nil {
		t.Fatal("short SRW key must be rejected by NewSRWWrapper")
	}
}

func TestSRWWrapperRoundTripAndClose(t *testing.T) {
	dk := mustDK(t)
	key := mustSRWKey(t)
	wr, err := NewSRWWrapper(key)
	if err != nil {
		t.Fatal(err)
	}
	// The wrapper copies the key: zeroizing the caller's buffer is safe.
	Zeroize(key)

	env, err := wr.WrapSRW(dk)
	if err != nil {
		t.Fatal(err)
	}
	got, err := wr.UnwrapSRW(env)
	if err != nil {
		t.Fatalf("wrapper round-trip: %v", err)
	}
	if !bytes.Equal(got, dk) {
		t.Fatal("wrapper round-trip mismatch")
	}

	wr.Close()
	if _, err := wr.UnwrapSRW(env); err == nil {
		t.Fatal("closed wrapper must not unwrap")
	}
}
