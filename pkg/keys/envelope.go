package keys

// Key-envelope wire format — LONG-TERM COMPATIBILITY PROMISE.
//
// The standalone decrypt CLI (Phase 5) must be able to parse every version of
// this format forever, using only the user's Recovery Key. The envelope is
// therefore fully self-describing: it carries its own KDF identity and
// parameters, salt, and nonce. Nothing outside the blob is needed to unwrap it.
//
// Byte layout (all integers big-endian):
//
//	offset  size  field
//	0       5     magic     "OCBKE" (OpenCloud BacKup Envelope)
//	5       1     version   format version (currently 1)
//	6       1     kind      1=RK, 2=SRW, 3=TW
//	7       1     kdf       0=none (direct 32-byte key), 1=Argon2id
//	8       4     argonTime      Argon2id time cost   (0 when kdf=none)
//	12      4     argonMemoryKiB Argon2id memory, KiB (0 when kdf=none)
//	16      1     argonLanes     Argon2id parallelism (0 when kdf=none)
//	17      1     saltLen   length of the KDF salt (0 when kdf=none)
//	18      varies salt     KDF salt
//	..      24    nonce     XChaCha20-Poly1305 nonce
//	..      rest  ciphertext AEAD(plaintext) with the header as additional data
//
// The full header (offsets 0..salt end) is passed to the AEAD as additional
// authenticated data, so version, kind, and KDF parameters are cryptographically
// bound to the ciphertext and cannot be downgraded or swapped.
//
// AEAD: XChaCha20-Poly1305 (24-byte random nonce — no counter state to manage,
// safe for random nonces at our volumes). KDF for the human-transportable RK:
// Argon2id. The SRW and TW keys are already 32 uniformly random bytes, so they
// are used directly (kdf=none) rather than stretched.

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	envelopeMagic     = "OCBKE"
	envelopeMagicLen  = 5
	envelopeHeaderMin = 18 // through saltLen, before the variable salt
	kekSize           = chacha20poly1305.KeySize
	nonceSize         = chacha20poly1305.NonceSizeX
)

// kdfID identifies the key-derivation function used to turn a secret into a KEK.
type kdfID uint8

const (
	// kdfNone uses the supplied 32-byte key directly (SRW / TW keys).
	kdfNone kdfID = 0
	// kdfArgon2id stretches a human-transportable secret (the RK).
	kdfArgon2id kdfID = 1
)

// ArgonParams are the Argon2id cost parameters recorded in an RK envelope.
// They are stored per envelope so parameters can be raised over time without
// breaking old envelopes (phase-3 testing requirement: "old envelopes still
// unwrap after a parameter bump").
type ArgonParams struct {
	// Time is the number of passes.
	Time uint32
	// MemoryKiB is the memory cost in KiB.
	MemoryKiB uint32
	// Lanes is the degree of parallelism.
	Lanes uint8
	// SaltLen is the salt length in bytes.
	SaltLen uint8
}

// Argon2id cost ceilings. An envelope carries its own KDF parameters, so a
// hostile or corrupted blob could otherwise ask us to spend effectively
// unbounded CPU/RAM deriving a key — a denial-of-service via the header, before
// any authentication can possibly fail. Parameters are therefore range-checked
// *before* derivation. The ceilings sit far above any legitimate setting so
// raising DefaultArgonParams stays possible without a format change.
const (
	maxArgonTime      = 16
	maxArgonMemoryKiB = 1 << 20 // 1 GiB
	maxArgonLanes     = 16
)

// DefaultArgonParams are the current Argon2id costs for RK-derived KEKs.
// Chosen for an interactive, browser-capable ceremony (Phase 8 runs the same
// KDF in WebAssembly): 64 MiB / 3 passes / 4 lanes is the widely recommended
// interactive baseline and stays feasible in-browser.
//
// Raising these is safe: new envelopes record the new costs, old envelopes keep
// unwrapping with theirs.
var DefaultArgonParams = ArgonParams{
	Time:      3,
	MemoryKiB: 64 * 1024,
	Lanes:     4,
	SaltLen:   16,
}

// Envelope errors. They are deliberately coarse: an unwrap failure must not
// reveal whether the key, the ciphertext, or the header was at fault, and must
// never echo key material.
var (
	// ErrBadEnvelope indicates a malformed or unsupported envelope.
	ErrBadEnvelope = errors.New("keys: malformed or unsupported envelope")
	// ErrUnwrap indicates authentication failure: wrong key or tampered blob.
	ErrUnwrap = errors.New("keys: cannot unwrap (wrong key or tampered envelope)")
)

// seal wraps plaintext under a secret, producing a self-contained envelope.
//
// When kdf is kdfArgon2id the secret is a human-transportable RK and is
// stretched with a fresh random salt; when kdf is kdfNone the secret must be a
// 32-byte uniformly random key (SRW/TW).
func seal(plaintext, secret []byte, kind WrapKind, kdf kdfID, params ArgonParams) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, fmt.Errorf("%w: empty plaintext", ErrBadEnvelope)
	}
	if len(secret) == 0 {
		return nil, fmt.Errorf("%w: empty secret", ErrBadEnvelope)
	}

	var salt []byte
	if kdf == kdfArgon2id {
		if params.SaltLen == 0 {
			return nil, fmt.Errorf("%w: zero salt length", ErrBadEnvelope)
		}
		salt = make([]byte, params.SaltLen)
		if _, err := rand.Read(salt); err != nil {
			return nil, fmt.Errorf("keys: salt: %w", err)
		}
	} else {
		if len(secret) != kekSize {
			return nil, fmt.Errorf("%w: direct key must be %d bytes", ErrBadEnvelope, kekSize)
		}
		params = ArgonParams{}
	}

	header := buildHeader(EnvelopeVersion, kind, kdf, params, salt)

	kek, err := deriveKEK(secret, salt, kdf, params)
	if err != nil {
		return nil, err
	}
	defer Zeroize(kek)

	aead, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return nil, fmt.Errorf("keys: aead: %w", err)
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("keys: nonce: %w", err)
	}

	// Header is authenticated (AAD) so parameters cannot be tampered with.
	out := make([]byte, 0, len(header)+nonceSize+len(plaintext)+aead.Overhead())
	out = append(out, header...)
	out = append(out, nonce...)
	out = aead.Seal(out, nonce, plaintext, header)
	return out, nil
}

// open unwraps an envelope with the given secret, returning the plaintext.
func open(blob, secret []byte) ([]byte, error) {
	hdr, nonce, ct, err := parseEnvelope(blob)
	if err != nil {
		return nil, err
	}

	kek, err := deriveKEK(secret, hdr.salt, hdr.kdf, hdr.params)
	if err != nil {
		return nil, err
	}
	defer Zeroize(kek)

	aead, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return nil, fmt.Errorf("keys: aead: %w", err)
	}
	pt, err := aead.Open(nil, nonce, ct, hdr.raw)
	if err != nil {
		// Never distinguish wrong-key from tampered-ciphertext, and never echo
		// any part of the key or blob.
		return nil, ErrUnwrap
	}
	return pt, nil
}

// envelopeHeader is the parsed, still-authenticated header of an envelope.
type envelopeHeader struct {
	version int
	kind    WrapKind
	kdf     kdfID
	params  ArgonParams
	salt    []byte
	// raw is the exact header bytes used as AEAD additional data.
	raw []byte
}

// buildHeader serializes the envelope header.
func buildHeader(version int, kind WrapKind, kdf kdfID, params ArgonParams, salt []byte) []byte {
	h := make([]byte, envelopeHeaderMin+len(salt))
	copy(h[0:envelopeMagicLen], envelopeMagic)
	h[5] = byte(version)
	h[6] = byte(kind)
	h[7] = byte(kdf)
	binary.BigEndian.PutUint32(h[8:12], params.Time)
	binary.BigEndian.PutUint32(h[12:16], params.MemoryKiB)
	h[16] = params.Lanes
	h[17] = byte(len(salt))
	copy(h[envelopeHeaderMin:], salt)
	return h
}

// parseEnvelope validates and splits an envelope into header, nonce, ciphertext.
func parseEnvelope(blob []byte) (envelopeHeader, []byte, []byte, error) {
	var h envelopeHeader
	if len(blob) < envelopeHeaderMin {
		return h, nil, nil, fmt.Errorf("%w: too short", ErrBadEnvelope)
	}
	if string(blob[0:envelopeMagicLen]) != envelopeMagic {
		return h, nil, nil, fmt.Errorf("%w: bad magic", ErrBadEnvelope)
	}
	h.version = int(blob[5])
	if h.version == 0 || h.version > EnvelopeVersion {
		return h, nil, nil, fmt.Errorf("%w: unsupported version %d", ErrBadEnvelope, h.version)
	}
	h.kind = WrapKind(blob[6])
	h.kdf = kdfID(blob[7])
	if h.kdf != kdfNone && h.kdf != kdfArgon2id {
		return h, nil, nil, fmt.Errorf("%w: unknown kdf %d", ErrBadEnvelope, h.kdf)
	}
	h.params = ArgonParams{
		Time:      binary.BigEndian.Uint32(blob[8:12]),
		MemoryKiB: binary.BigEndian.Uint32(blob[12:16]),
		Lanes:     blob[16],
		SaltLen:   blob[17],
	}
	// Reject hostile KDF costs here, before any derivation is attempted.
	if h.kdf == kdfArgon2id {
		if err := validateArgonParams(h.params); err != nil {
			return h, nil, nil, err
		}
	}
	saltLen := int(blob[17])
	headerLen := envelopeHeaderMin + saltLen
	if len(blob) < headerLen+nonceSize+chacha20poly1305.Overhead {
		return h, nil, nil, fmt.Errorf("%w: truncated", ErrBadEnvelope)
	}
	h.salt = blob[envelopeHeaderMin:headerLen]
	h.raw = blob[:headerLen]

	nonce := blob[headerLen : headerLen+nonceSize]
	ct := blob[headerLen+nonceSize:]
	return h, nonce, ct, nil
}

// validateArgonParams rejects absent or absurd Argon2id costs. It is applied to
// every envelope before any derivation happens, so a crafted header cannot turn
// an unwrap attempt into a resource-exhaustion attack.
func validateArgonParams(p ArgonParams) error {
	switch {
	case p.Time == 0 || p.MemoryKiB == 0 || p.Lanes == 0:
		return fmt.Errorf("%w: invalid argon2id parameters", ErrBadEnvelope)
	case p.Time > maxArgonTime:
		return fmt.Errorf("%w: argon2id time cost out of range", ErrBadEnvelope)
	case p.MemoryKiB > maxArgonMemoryKiB:
		return fmt.Errorf("%w: argon2id memory cost out of range", ErrBadEnvelope)
	case p.Lanes > maxArgonLanes:
		return fmt.Errorf("%w: argon2id parallelism out of range", ErrBadEnvelope)
	}
	return nil
}

// deriveKEK turns a secret into the 32-byte AEAD key.
func deriveKEK(secret, salt []byte, kdf kdfID, params ArgonParams) ([]byte, error) {
	switch kdf {
	case kdfNone:
		if len(secret) != kekSize {
			return nil, fmt.Errorf("%w: direct key must be %d bytes", ErrBadEnvelope, kekSize)
		}
		kek := make([]byte, kekSize)
		copy(kek, secret)
		return kek, nil
	case kdfArgon2id:
		if err := validateArgonParams(params); err != nil {
			return nil, err
		}
		return argon2.IDKey(secret, salt, params.Time, params.MemoryKiB, params.Lanes, kekSize), nil
	default:
		return nil, fmt.Errorf("%w: unknown kdf", ErrBadEnvelope)
	}
}

// EnvelopeInfo describes an envelope without revealing key material. It is safe
// to log or expose in a status response.
type EnvelopeInfo struct {
	Version int
	Kind    WrapKind
	KDF     string
	Argon   ArgonParams
}

// Inspect parses an envelope header and reports its metadata. It performs no
// decryption and needs no key — used by keystatus and by the decrypt CLI to
// show what a blob needs before prompting for the Recovery Key.
func Inspect(blob []byte) (EnvelopeInfo, error) {
	h, _, _, err := parseEnvelope(blob)
	if err != nil {
		return EnvelopeInfo{}, err
	}
	kdfName := "none"
	if h.kdf == kdfArgon2id {
		kdfName = "argon2id"
	}
	return EnvelopeInfo{
		Version: h.version,
		Kind:    h.kind,
		KDF:     kdfName,
		Argon:   h.params,
	}, nil
}

// Zeroize overwrites a key buffer in place. Go's GC may still have copied the
// memory, so this is best-effort defence-in-depth, not a guarantee (AGENTS.md:
// "zeroized after use where Go allows").
func Zeroize(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
