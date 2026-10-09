package keys

// Fuzz targets for the formats this package promises to parse forever: the key
// envelope (key-envelope-format.md) and the Recovery Key's display form.
//
// The seeds are the golden vectors in testdata/vectors.json, so `go test` runs
// every target over them as a plain unit test; `make fuzz` (CI: seconds per
// target, nightly: minutes) explores from there. A crasher is saved under
// testdata/fuzz/<Target>/ and stays a regression case once committed.
//
// What the targets hold, beyond "does not panic":
//   - every failure is one of the package's coarse sentinels, and a Recovery
//     Key error never carries the input (AGENTS.md: never log key material);
//   - a header that parses re-serialises to exactly the bytes it came from,
//     so no field is read and then ignored;
//   - a blob that opens with the fixed key opens with nothing else.

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"testing"
)

// fuzzArgonCeiling bounds the Argon2id work a single fuzz input may ask for.
// The envelope's own ceilings (1 GiB, 16 passes) are right for production and
// far too slow for thousands of executions a second; a blob above this is still
// parsed and inspected, only not opened.
var fuzzArgonCeiling = ArgonParams{Time: 2, MemoryKiB: 256, Lanes: 4}

// cheapArgon are the costs of the fuzz-only Argon2id seed: small enough to open
// on every execution, so mutations of a real RK envelope reach the AEAD.
var cheapArgon = ArgonParams{Time: 1, MemoryKiB: 64, Lanes: 1, SaltLen: 16}

// fuzzSecrets returns the fixed secrets every opened input is tried with: the
// direct key of the vectors' kdf=none envelope and the raw entropy of one of
// their Recovery Keys. Taking both from the vectors means the seeds open.
func fuzzSecrets(f *testing.F, file vectorFile) (direct, rk []byte) {
	f.Helper()
	for _, v := range file.Envelopes {
		switch v.KDF {
		case "none":
			direct = mustHex(v.SecretHex)
		case "argon2id":
			rk = mustHex(v.SecretHex)
		}
	}
	if len(direct) != kekSize || len(rk) != rkEntropyBytes {
		f.Fatal("vectors.json lacks a kdf=none or an argon2id envelope to take fixed secrets from")
	}
	return direct, rk
}

func FuzzEnvelope(f *testing.F) {
	file := readVectors(f)
	direct, rk := fuzzSecrets(f, file)

	for _, v := range file.Envelopes {
		f.Add(mustHex(v.EnvelopeHex))
	}
	// An RK envelope cheap enough to open, so the fuzzer can mutate one that
	// gets past Argon2id instead of only ones the ceiling skips.
	cheap, err := sealWith(bytes.NewReader(make([]byte, 64)), []byte("fuzz seed plaintext"), rk, WrapRK, kdfArgon2id, cheapArgon)
	if err != nil {
		f.Fatalf("seal cheap seed: %v", err)
	}
	f.Add(cheap)
	f.Add([]byte(envelopeMagic))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, blob []byte) {
		info, inspectErr := Inspect(blob)
		_, checkErr := CheckRecoveryEnvelope(blob)

		if inspectErr != nil {
			if !errors.Is(inspectErr, ErrBadEnvelope) {
				t.Fatalf("Inspect error %v is not ErrBadEnvelope", inspectErr)
			}
			if checkErr == nil {
				t.Fatal("CheckRecoveryEnvelope accepted a blob Inspect refused")
			}
			if _, err := open(blob, direct); !errors.Is(err, ErrBadEnvelope) {
				t.Fatalf("open of an uninspectable blob: err = %v, want ErrBadEnvelope", err)
			}
			return
		}

		assertInspected(t, info)
		assertHeaderRoundTrips(t, blob)
		if checkErr == nil {
			assertAcceptedBySetup(t, info)
		} else if !errors.Is(checkErr, ErrBadEnvelope) && !errors.Is(checkErr, ErrWeakEnvelope) {
			t.Fatalf("CheckRecoveryEnvelope error %v is neither ErrBadEnvelope nor ErrWeakEnvelope", checkErr)
		}

		secret := direct
		if info.KDF == "argon2id" {
			if !withinFuzzCeiling(info.Argon) {
				return
			}
			secret = rk
		}
		assertOpensOnlyWith(t, blob, secret)
	})
}

// assertInspected checks what Inspect promises about a blob it accepted.
func assertInspected(t *testing.T, info EnvelopeInfo) {
	t.Helper()
	if info.Version < 1 || info.Version > EnvelopeVersion {
		t.Fatalf("Inspect accepted version %d", info.Version)
	}
	switch info.KDF {
	case "none":
	case "argon2id":
		if err := validateArgonParams(info.Argon); err != nil {
			t.Fatalf("Inspect accepted argon2id costs it should refuse: %+v", info.Argon)
		}
	default:
		t.Fatalf("Inspect reported kdf %q", info.KDF)
	}
}

// assertHeaderRoundTrips serialises the parsed header again. Every header byte
// is authenticated, so a field the parser reads and then drops would make two
// different blobs look alike to every caller that trusts Inspect.
func assertHeaderRoundTrips(t *testing.T, blob []byte) {
	t.Helper()
	h, _, _, err := parseEnvelope(blob)
	if err != nil {
		t.Fatalf("parseEnvelope refused what Inspect accepted: %v", err)
	}
	if got := buildHeader(h.version, h.kind, h.kdf, h.params, h.salt); !bytes.Equal(got, h.raw) {
		t.Fatalf("header does not round-trip:\n got %x\nwant %x", got, h.raw)
	}
}

// assertAcceptedBySetup checks the policy CheckRecoveryEnvelope enforces.
func assertAcceptedBySetup(t *testing.T, info EnvelopeInfo) {
	t.Helper()
	if info.Kind != WrapRK || info.KDF != "argon2id" {
		t.Fatalf("setup would store a %v/%s envelope as a recovery envelope", info.Kind, info.KDF)
	}
	if err := checkArgonFloor(info.Argon); err != nil {
		t.Fatalf("setup would store an envelope below the floor: %+v", info.Argon)
	}
}

// assertOpensOnlyWith tries the fixed secret. Failing is the normal outcome for
// a mutated blob and must be the coarse ErrUnwrap; succeeding must not survive
// a different secret.
func assertOpensOnlyWith(t *testing.T, blob, secret []byte) {
	t.Helper()
	pt, err := open(blob, secret)
	if err != nil {
		if err != ErrUnwrap { //nolint:errorlint // the unwrap failure must be the bare sentinel, nothing wrapped around it
			t.Fatalf("open error %v is not the bare ErrUnwrap", err)
		}
		return
	}
	if len(pt) == 0 {
		t.Fatal("open returned an empty plaintext without an error")
	}
	other := bytes.Clone(secret)
	other[0] ^= 0xff
	if _, err := open(blob, other); err != ErrUnwrap { //nolint:errorlint // see above
		t.Fatalf("blob also opened with a different secret: err = %v", err)
	}
}

func withinFuzzCeiling(p ArgonParams) bool {
	return p.Time <= fuzzArgonCeiling.Time &&
		p.MemoryKiB <= fuzzArgonCeiling.MemoryKiB &&
		p.Lanes <= fuzzArgonCeiling.Lanes
}

func FuzzDecodeRecoveryKey(f *testing.F) {
	file := readVectors(f)
	for _, v := range file.RecoveryKeys {
		f.Add(v.Display)
	}
	for _, v := range file.RecoveryDecodes {
		f.Add(v.Input)
	}

	f.Fuzz(func(t *testing.T, s string) {
		entropy, err := DecodeRecoveryKey(s)
		if err != nil {
			assertCoarseDecodeError(t, err)
			return
		}
		if len(entropy) != rkEntropyBytes {
			t.Fatalf("decoded %d bytes, want %d", len(entropy), rkEntropyBytes)
		}
		again, err := DecodeRecoveryKey(EncodeRecoveryKey(entropy))
		if err != nil || !bytes.Equal(again, entropy) {
			t.Fatalf("the canonical form of an accepted key does not decode to the same bytes (err %v)", err)
		}
	})
}

// assertCoarseDecodeError holds DecodeRecoveryKey to its promise that a
// mistyped key never reaches a log: the message is the sentinel plus one of the
// known reasons, so nothing of the input can be in it.
func assertCoarseDecodeError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrBadRecoveryKey) {
		t.Fatalf("error %v is not ErrBadRecoveryKey", err)
	}
	reason, ok := strings.CutPrefix(err.Error(), ErrBadRecoveryKey.Error()+": ")
	if !ok {
		t.Fatalf("error %q does not start with the sentinel", err)
	}
	reason = strings.TrimSuffix(reason, " (mistyped?)")
	for _, known := range decodeReasons {
		if reason == known {
			return
		}
	}
	t.Fatalf("error %q carries an unknown reason", err)
}

// displayForm is the shape EncodeRecoveryKey must produce: the version prefix
// and six groups of five plus one of four Crockford characters.
var displayForm = regexp.MustCompile(`^ocbk1(-[0-9A-HJKMNP-TV-Z]{5}){6}-[0-9A-HJKMNP-TV-Z]{4}$`)

func FuzzRecoveryKeyRoundTrip(f *testing.F) {
	for _, v := range readVectors(f).RecoveryKeys {
		f.Add(mustHex(v.EntropyHex))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		entropy := make([]byte, rkEntropyBytes)
		copy(entropy, raw)

		display := EncodeRecoveryKey(entropy)
		if !displayForm.MatchString(display) {
			t.Fatalf("display form %q has the wrong shape", display)
		}
		// The variants a person produces when retyping the key.
		for _, typed := range []string{
			display,
			strings.ToLower(display),
			strings.ReplaceAll(display, "-", ""),
			strings.ReplaceAll(display, "-", " "),
			strings.TrimPrefix(display, RKPrefix+"-"),
			"  " + display + "\n",
		} {
			got, err := DecodeRecoveryKey(typed)
			if err != nil {
				t.Fatalf("a retyped form of an encoded key is refused: %v", err)
			}
			if !bytes.Equal(got, entropy) {
				t.Fatal("a retyped form of an encoded key decodes to different bytes")
			}
		}
	})
}
