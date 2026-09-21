package keys

// Cross-implementation test vectors — the pin that keeps the browser client and
// this package byte-compatible.
//
// The Phase-8 web UI performs the key ceremony client-side: it generates the
// Recovery Key, derives the KEK with Argon2id compiled to WebAssembly, and seals
// the Data Key with XChaCha20-Poly1305. Nothing in the running system compares
// those bytes against Go's — the server stores whatever envelope it is handed
// and answers 201. A parameter mismatch therefore surfaces years later, in the
// one situation the whole project exists for: a user reaching for their Recovery
// Key after losing everything else.
//
// testdata/vectors.json is that comparison, made early and made explicit. Both
// implementations derive the same outputs from the same inputs and check them
// field by field; see web/src/crypto/vectors.spec.ts for the other side.
//
// Regenerate with:
//
//	go test ./pkg/keys -run TestGoldenVectors -update
//
// Regenerating is only ever correct for a *new* vector. If an existing envelope
// changes, the wire format changed, and that is a compatibility break the
// decrypt CLI must keep parsing forever (key-envelope-format.md).

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateVectors = flag.Bool("update", false, "rewrite testdata/vectors.json from the in-source table")

const vectorsPath = "testdata/vectors.json"

// vectorFile is the on-disk shape. Every field is either an input both
// implementations feed in, or an output both must produce.
type vectorFile struct {
	Comment         string           `json:"_comment"`
	EnvelopeVersion int              `json:"envelope_version"`
	Magic           string           `json:"magic"`
	RKPrefix        string           `json:"rk_prefix"`
	MinArgon        argonJSON        `json:"min_argon_params"`
	DefaultArgon    argonJSON        `json:"default_argon_params"`
	RecoveryKeys    []rkVector       `json:"recovery_keys"`
	RecoveryDecodes []rkDecodeVector `json:"recovery_key_decodes"`
	Envelopes       []envelopeVector `json:"envelopes"`
}

type argonJSON struct {
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Lanes     uint8  `json:"lanes"`
	SaltLen   uint8  `json:"salt_len"`
}

// rkVector pins the entropy-to-display-string encoding.
type rkVector struct {
	Name       string `json:"name"`
	EntropyHex string `json:"entropy_hex"`
	Display    string `json:"display"`
}

// rkDecodeVector pins the tolerant decoder: what a user may type, and what must
// be refused. Exactly one of EntropyHex/Error is set.
//
// Error is a reason code rather than a message. Two implementations will never
// phrase an error identically, but they must agree on *why* a key was refused —
// a key rejected for the wrong reason tells the user the wrong thing, and
// "mistyped" versus "wrong key" is the difference between retrying and giving
// up.
type rkDecodeVector struct {
	Name       string `json:"name"`
	Input      string `json:"input"`
	EntropyHex string `json:"entropy_hex,omitempty"`
	Error      string `json:"error,omitempty"`
}

// decodeReasons maps a vector's reason code to the substring this package's
// error carries. The browser has its own map to its own codes.
var decodeReasons = map[string]string{
	"length":   "wrong length",
	"version":  "unsupported recovery key version",
	"charset":  "illegal character",
	"checksum": "checksum mismatch",
	"padding":  "trailing bits",
}

// envelopeVector pins the sealed bytes. Salt and nonce are inputs here; in
// production both come from the operating system.
type envelopeVector struct {
	Name string `json:"name"`
	Note string `json:"note"`

	Kind  int       `json:"kind"`
	KDF   string    `json:"kdf"`
	Argon argonJSON `json:"argon,omitzero"`

	// SecretHex is the wrapping secret: the RK's raw entropy for argon2id
	// envelopes, a 32-byte key for kdf=none.
	SecretHex    string `json:"secret_hex"`
	RKDisplay    string `json:"rk_display,omitempty"`
	SaltHex      string `json:"salt_hex"`
	NonceHex     string `json:"nonce_hex"`
	PlaintextHex string `json:"plaintext_hex"`

	KEKHex      string `json:"kek_hex"`
	HeaderHex   string `json:"header_hex"`
	EnvelopeHex string `json:"envelope_hex"`
	EnvelopeB64 string `json:"envelope_b64"`

	// AcceptedBySetup records whether POST /backup/setup would take this
	// envelope, so the browser's "is this strong enough" check can be pinned
	// against the same list.
	AcceptedBySetup bool `json:"accepted_by_setup"`
}

// vectorInputs is the source of truth. testdata/vectors.json is generated from
// it, never edited by hand.
type vectorInputs struct {
	recoveryKeys []rkVector
	decodes      []rkDecodeVector
	envelopes    []envelopeVector
}

func inputs() vectorInputs {
	// Entropy values are fixed patterns, not random: a vector nobody can
	// reproduce from the file alone is a vector nobody can debug.
	var (
		entropyCounting = mustHex("000102030405060708090a0b0c0d0e0f10111213")
		entropyZero     = bytes.Repeat([]byte{0x00}, rkEntropyBytes)
		entropyMax      = bytes.Repeat([]byte{0xff}, rkEntropyBytes)
		entropyMixed    = mustHex("d7a4f1093b62c85e0a7714fd3390bb2c6e51470d")
	)

	dk := mustHex("404142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f")
	srwKey := mustHex("8f8e8d8c8b8a89888786858483828180 7f7e7d7c7b7a79787776757473727170")

	return vectorInputs{
		recoveryKeys: []rkVector{
			{Name: "counting", EntropyHex: hex.EncodeToString(entropyCounting)},
			{Name: "all-zero", EntropyHex: hex.EncodeToString(entropyZero)},
			{Name: "all-ones", EntropyHex: hex.EncodeToString(entropyMax)},
			{Name: "mixed", EntropyHex: hex.EncodeToString(entropyMixed)},
		},
		decodes: []rkDecodeVector{
			{
				Name:       "canonical",
				Input:      EncodeRecoveryKey(entropyCounting),
				EntropyHex: hex.EncodeToString(entropyCounting),
			},
			{
				Name:       "lowercase",
				Input:      lower(EncodeRecoveryKey(entropyCounting)),
				EntropyHex: hex.EncodeToString(entropyCounting),
			},
			{
				Name:       "no dashes, no prefix",
				Input:      stripDashes(EncodeRecoveryKey(entropyCounting))[len(RKPrefix):],
				EntropyHex: hex.EncodeToString(entropyCounting),
			},
			{
				Name:       "surrounding whitespace and inner spaces",
				Input:      "  " + spaceForDash(EncodeRecoveryKey(entropyCounting)) + "\n",
				EntropyHex: hex.EncodeToString(entropyCounting),
			},
			{
				// Handwriting a key is the reason the look-alike mapping
				// exists; "OI" typed for "01" must still open the envelope.
				Name:       "look-alike characters",
				Input:      lookAlikes(EncodeRecoveryKey(entropyZero)),
				EntropyHex: hex.EncodeToString(entropyZero),
			},
			{Name: "empty", Input: "", Error: "length"},
			{Name: "future version prefix", Input: "ocbk2-000G4-0R40M-30E20-9185G-R38E1-W8124-GKWW", Error: "version"},
			{Name: "illegal character", Input: "ocbk1-000G4-0R40M-30E20-9185G-R38E1-W8124-GKWU", Error: "charset"},
			{Name: "too short", Input: "ocbk1-000G4-0R40M", Error: "length"},
			// Changing the last character to another one whose low two bits are
			// zero gets past the padding check, so the checksum is what catches
			// it. That is the split these two vectors exist to hold apart.
			{Name: "checksum mismatch", Input: "ocbk1-000G4-0R40M-30E20-9185G-R38E1-W8124-GKWR", Error: "checksum"},
			{Name: "non-zero trailing bits", Input: "ocbk1-000G4-0R40M-30E20-9185G-R38E1-W8124-GKWX", Error: "padding"},
		},
		envelopes: []envelopeVector{
			{
				Name:         "rk-default-params",
				Note:         "DefaultArgonParams: what the browser ceremony uses unless a device is too slow.",
				Kind:         int(WrapRK),
				KDF:          "argon2id",
				Argon:        toArgonJSON(DefaultArgonParams),
				SecretHex:    hex.EncodeToString(entropyCounting),
				SaltHex:      "a0a1a2a3a4a5a6a7a8a9aaabacadaeaf",
				NonceHex:     "101112131415161718191a1b1c1d1e1f2021222324252627",
				PlaintextHex: hex.EncodeToString(dk),
			},
			{
				Name:         "rk-policy-floor",
				Note:         "MinArgonParams: the weakest envelope the API accepts, and the one the test suites use so they stay fast.",
				Kind:         int(WrapRK),
				KDF:          "argon2id",
				Argon:        toArgonJSON(MinArgonParams),
				SecretHex:    hex.EncodeToString(entropyMixed),
				SaltHex:      "000102030405060708090a0b0c0d0e0f",
				NonceHex:     "c0c1c2c3c4c5c6c7c8c9cacbcccdcecfd0d1d2d3d4d5d6d7",
				PlaintextHex: hex.EncodeToString(dk),
			},
			{
				Name:         "rk-32-byte-salt",
				Note:         "A salt longer than the default moves every field after it; the header is variable-length and both sides must treat it that way.",
				Kind:         int(WrapRK),
				KDF:          "argon2id",
				Argon:        argonJSON{Time: 2, MemoryKiB: 19 * 1024, Lanes: 2, SaltLen: 32},
				SecretHex:    hex.EncodeToString(entropyZero),
				SaltHex:      "0f0e0d0c0b0a09080706050403020100101112131415161718191a1b1c1d1e1f",
				NonceHex:     "202122232425262728292a2b2c2d2e2f3031323334353637",
				PlaintextHex: hex.EncodeToString(dk),
			},
			{
				Name:         "rk-below-floor",
				Note:         "Parseable and unwrappable, but refused by POST /backup/setup with 400. The browser must refuse to produce it in the first place.",
				Kind:         int(WrapRK),
				KDF:          "argon2id",
				Argon:        argonJSON{Time: 1, MemoryKiB: 8 * 1024, Lanes: 1, SaltLen: 16},
				SecretHex:    hex.EncodeToString(entropyCounting),
				SaltHex:      "ffeeddccbbaa99887766554433221100",
				NonceHex:     "0102030405060708090a0b0c0d0e0f101112131415161718",
				PlaintextHex: hex.EncodeToString(dk),
			},
			{
				Name:         "srw-direct-key",
				Note:         "kdf=none, kind=SRW. The browser never produces one; it is here so the fixed 18-byte header form is pinned too.",
				Kind:         int(WrapSRW),
				KDF:          "none",
				SecretHex:    hex.EncodeToString(srwKey),
				PlaintextHex: hex.EncodeToString(dk),
				NonceHex:     "112233445566778899aabbccddeeff000102030405060708",
			},
		},
	}
}

// TestGoldenVectors verifies (or, with -update, regenerates) testdata/vectors.json.
func TestGoldenVectors(t *testing.T) {
	got := buildVectors(t)

	if *updateVectors {
		writeVectors(t, got)
		t.Logf("wrote %s", vectorsPath)
		return
	}

	want := readVectors(t)
	gotJSON := marshal(t, got)
	wantJSON := marshal(t, want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("%s is stale or the wire format changed.\n"+
			"If a vector was added, rerun with -update. If an existing envelope moved, "+
			"the format changed and old envelopes stop unwrapping — see key-envelope-format.md.\n"+
			"got:\n%s\nwant:\n%s", vectorsPath, gotJSON, wantJSON)
	}
}

// TestVectorsUnwrap opens every committed envelope, so the file proves it is
// still openable and not merely unchanged.
func TestVectorsUnwrap(t *testing.T) {
	file := readVectors(t)

	for _, v := range file.Envelopes {
		t.Run(v.Name, func(t *testing.T) {
			blob := mustHex(v.EnvelopeHex)
			secret := mustHex(v.SecretHex)

			pt, err := open(blob, secret)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if want := mustHex(v.PlaintextHex); !bytes.Equal(pt, want) {
				t.Fatalf("plaintext = %x, want %x", pt, want)
			}

			info, err := Inspect(blob)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if info.Version != EnvelopeVersion || int(info.Kind) != v.Kind || info.KDF != v.KDF {
				t.Fatalf("info = %+v, want version %d kind %d kdf %q", info, EnvelopeVersion, v.Kind, v.KDF)
			}

			_, err = CheckRecoveryEnvelope(blob)
			if accepted := err == nil; accepted != v.AcceptedBySetup {
				t.Fatalf("CheckRecoveryEnvelope accepted = %v (%v), want %v", accepted, err, v.AcceptedBySetup)
			}
		})
	}

	for _, v := range file.RecoveryKeys {
		t.Run("rk/"+v.Name, func(t *testing.T) {
			entropy, err := DecodeRecoveryKey(v.Display)
			if err != nil {
				t.Fatalf("DecodeRecoveryKey: %v", err)
			}
			if want := mustHex(v.EntropyHex); !bytes.Equal(entropy, want) {
				t.Fatalf("entropy = %x, want %x", entropy, want)
			}
		})
	}

	for _, v := range file.RecoveryDecodes {
		t.Run("decode/"+v.Name, func(t *testing.T) {
			entropy, err := DecodeRecoveryKey(v.Input)
			if v.Error != "" {
				if !errors.Is(err, ErrBadRecoveryKey) {
					t.Fatalf("err = %v, want ErrBadRecoveryKey (%s)", err, v.Error)
				}
				want, known := decodeReasons[v.Error]
				if !known {
					t.Fatalf("vector uses reason code %q, which no implementation knows", v.Error)
				}
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %v, want reason %q (%s)", err, want, v.Error)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeRecoveryKey: %v", err)
			}
			if want := mustHex(v.EntropyHex); !bytes.Equal(entropy, want) {
				t.Fatalf("entropy = %x, want %x", entropy, want)
			}
		})
	}
}

// buildVectors computes every output field from the in-source inputs.
func buildVectors(t *testing.T) vectorFile {
	t.Helper()
	in := inputs()

	file := vectorFile{
		Comment: "Generated by `go test ./pkg/keys -run TestGoldenVectors -update`. " +
			"Read by the Go tests in this package and by web/src/crypto/vectors.spec.ts. " +
			"Do not edit by hand.",
		EnvelopeVersion: EnvelopeVersion,
		Magic:           envelopeMagic,
		RKPrefix:        RKPrefix,
		MinArgon:        toArgonJSON(MinArgonParams),
		DefaultArgon:    toArgonJSON(DefaultArgonParams),
		RecoveryDecodes: in.decodes,
	}

	for _, rk := range in.recoveryKeys {
		rk.Display = EncodeRecoveryKey(mustHex(rk.EntropyHex))
		file.RecoveryKeys = append(file.RecoveryKeys, rk)
	}

	for _, v := range in.envelopes {
		secret := mustHex(v.SecretHex)
		salt := mustHex(v.SaltHex)
		nonce := mustHex(v.NonceHex)
		plaintext := mustHex(v.PlaintextHex)

		kdf := kdfNone
		params := ArgonParams{}
		if v.KDF == "argon2id" {
			kdf = kdfArgon2id
			params = fromArgonJSON(v.Argon)
			if int(params.SaltLen) != len(salt) {
				t.Fatalf("%s: salt_len %d but salt is %d bytes", v.Name, params.SaltLen, len(salt))
			}
			v.RKDisplay = EncodeRecoveryKey(secret)
		}

		blob, err := sealWith(bytes.NewReader(concat(salt, nonce)), plaintext, secret, WrapKind(v.Kind), kdf, params)
		if err != nil {
			t.Fatalf("%s: sealWith: %v", v.Name, err)
		}
		kek, err := deriveKEK(secret, salt, kdf, params)
		if err != nil {
			t.Fatalf("%s: deriveKEK: %v", v.Name, err)
		}

		headerLen := envelopeHeaderMin + len(salt)
		v.KEKHex = hex.EncodeToString(kek)
		v.HeaderHex = hex.EncodeToString(blob[:headerLen])
		v.EnvelopeHex = hex.EncodeToString(blob)
		v.EnvelopeB64 = base64.StdEncoding.EncodeToString(blob)
		_, err = CheckRecoveryEnvelope(blob)
		v.AcceptedBySetup = err == nil

		file.Envelopes = append(file.Envelopes, v)
	}

	return file
}

func readVectors(t *testing.T) vectorFile {
	t.Helper()
	raw, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatalf("read %s (regenerate with -update): %v", vectorsPath, err)
	}
	var file vectorFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse %s: %v", vectorsPath, err)
	}
	return file
}

func writeVectors(t *testing.T, file vectorFile) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(vectorsPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(vectorsPath, append(marshal(t, file), '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", vectorsPath, err)
	}
}

func marshal(t *testing.T, file vectorFile) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// The two Argon structs are field-identical: ArgonParams is the internal shape,
// argonJSON the wire one. A direct conversion keeps them that way — adding a
// field to one and not the other stops compiling here.
func toArgonJSON(p ArgonParams) argonJSON { return argonJSON(p) }

func fromArgonJSON(p argonJSON) ArgonParams { return ArgonParams(p) }

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// mustHex decodes a hex string, ignoring spaces so long constants can be
// grouped for reading.
func mustHex(s string) []byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		panic("keys: bad hex in test vector: " + err.Error())
	}
	return b
}

// The helpers below build the "what a user might type" decode vectors. They
// deliberately mangle a canonical key rather than hard-coding a second string,
// so the mangling stays tied to the key it came from.

func lower(s string) string { return strings.ToLower(s) }

func stripDashes(s string) string { return strings.ReplaceAll(s, "-", "") }

func spaceForDash(s string) string { return strings.ReplaceAll(s, "-", " ") }

// lookAlikes rewrites the payload the way handwriting does — 0 as O, 1 as I —
// leaving the version prefix alone, because a mangled prefix is a different
// failure (an unsupported version) and is covered by its own vector.
func lookAlikes(s string) string {
	prefix, payload, found := strings.Cut(s, "-")
	if !found {
		panic("keys: recovery key without a prefix separator")
	}
	payload = strings.ReplaceAll(payload, "0", "O")
	payload = strings.ReplaceAll(payload, "1", "I")
	return prefix + "-" + payload
}
