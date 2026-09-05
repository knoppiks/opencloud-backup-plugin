package keys

// Recovery Key encoding — LONG-TERM COMPATIBILITY PROMISE.
//
// The RK is the only thing that can decrypt a user's backup (decisions.md), so
// its textual form must stay parseable by the offline decrypt CLI forever, and
// must be typable from a password manager.
//
// Format:
//
//	ocbk1-XXXXX-XXXXX-XXXXX-XXXXX-XXXXX-XXXXX-XXXXX
//
//   - "ocbk1" is a versioned prefix; a future format bumps it to ocbk2.
//   - The payload is 160 bits of entropy + an 8-bit checksum = 168 bits,
//     encoded as 35 Crockford base32 characters in 7 groups of 5.
//   - Crockford base32 excludes I, L, O, and U, so it survives transcription;
//     decoding is case-insensitive and maps the look-alikes I/L->1 and O->0.
//   - The checksum catches typos before an expensive Argon2id derivation and
//     lets the UI say "that key is mistyped" rather than "wrong key".
//
// 160 bits of entropy is far beyond brute-force reach even though Argon2id also
// stretches it; the KDF exists to slow down attacks on the wrapped blob, not to
// compensate for weak entropy.
//
// The plaintext RK is generated client-side (Phase 8, WebCrypto) and never
// crosses the network. This file lives server-side only because the decrypt CLI
// links the same package.

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
)

const (
	// RKPrefix is the versioned Recovery Key prefix.
	RKPrefix = "ocbk1"
	// rkEntropyBytes is the raw entropy carried by an RK (160 bits).
	rkEntropyBytes = 20
	// rkGroupSize is the number of characters per dash-separated group.
	rkGroupSize = 5
	// crockford is the Crockford base32 alphabet (no I, L, O, U).
	crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

// ErrBadRecoveryKey indicates a malformed, mistyped, or wrong-version RK.
var ErrBadRecoveryKey = errors.New("keys: invalid recovery key")

// GenerateRecoveryKey returns a fresh Recovery Key: the human-transportable
// string to show the user once, plus the raw secret bytes used for wrapping.
//
// The caller must show the string exactly once and never persist it server-side
// (decisions.md: no server-side RK escrow). Zeroize the returned secret when
// done.
func GenerateRecoveryKey() (display string, secret []byte, err error) {
	entropy := make([]byte, rkEntropyBytes)
	if _, err := rand.Read(entropy); err != nil {
		return "", nil, fmt.Errorf("keys: generate recovery key: %w", err)
	}
	return EncodeRecoveryKey(entropy), entropy, nil
}

// EncodeRecoveryKey renders raw entropy as the display form.
func EncodeRecoveryKey(entropy []byte) string {
	payload := make([]byte, 0, len(entropy)+1)
	payload = append(payload, entropy...)
	payload = append(payload, checksumByte(entropy))

	encoded := base32Encode(payload)
	var b strings.Builder
	b.WriteString(RKPrefix)
	for i := 0; i < len(encoded); i += rkGroupSize {
		end := min(i+rkGroupSize, len(encoded))
		b.WriteByte('-')
		b.WriteString(encoded[i:end])
	}
	return b.String()
}

// DecodeRecoveryKey parses a user-entered Recovery Key back into the raw secret
// used for wrapping. It is tolerant of case, spaces, and missing/extra dashes,
// and maps Crockford look-alike characters, because users retype these by hand.
//
// It never includes the input in its error, so a mistyped key cannot leak into
// logs (AGENTS.md: never log key material).
func DecodeRecoveryKey(s string) ([]byte, error) {
	cleaned := strings.TrimSpace(s)
	// Accept an optional, case-insensitive version prefix.
	lower := strings.ToLower(cleaned)
	switch {
	case strings.HasPrefix(lower, RKPrefix+"-"):
		cleaned = cleaned[len(RKPrefix)+1:]
	case strings.HasPrefix(lower, RKPrefix):
		cleaned = cleaned[len(RKPrefix):]
	default:
		// A different version prefix is an explicit, actionable failure.
		if i := strings.Index(lower, "-"); i > 0 && strings.HasPrefix(lower, "ocbk") {
			return nil, fmt.Errorf("%w: unsupported recovery key version", ErrBadRecoveryKey)
		}
	}

	var sb strings.Builder
	for _, r := range strings.ToUpper(cleaned) {
		switch r {
		case '-', ' ', '\t', '\n', '\r':
			continue
		case 'I', 'L':
			sb.WriteByte('1')
		case 'O':
			sb.WriteByte('0')
		default:
			if strings.ContainsRune(crockford, r) {
				sb.WriteRune(r)
				continue
			}
			return nil, fmt.Errorf("%w: illegal character", ErrBadRecoveryKey)
		}
	}

	payload, err := base32Decode(sb.String())
	if err != nil {
		return nil, err
	}
	if len(payload) != rkEntropyBytes+1 {
		return nil, fmt.Errorf("%w: wrong length", ErrBadRecoveryKey)
	}
	entropy := payload[:rkEntropyBytes]
	if payload[rkEntropyBytes] != checksumByte(entropy) {
		return nil, fmt.Errorf("%w: checksum mismatch (mistyped?)", ErrBadRecoveryKey)
	}
	return entropy, nil
}

// checksumByte is a truncated SHA-256 over the entropy, used only to detect
// transcription errors — it is not a security control.
func checksumByte(entropy []byte) byte {
	sum := sha256.Sum256(entropy)
	return sum[0]
}

// base32Encode encodes bytes MSB-first using the Crockford alphabet, without
// padding. Length is ceil(len(b)*8/5).
func base32Encode(b []byte) string {
	var out strings.Builder
	var acc uint32
	var bits uint
	for _, c := range b {
		acc = acc<<8 | uint32(c)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out.WriteByte(crockford[(acc>>bits)&0x1f])
		}
	}
	if bits > 0 {
		out.WriteByte(crockford[(acc<<(5-bits))&0x1f])
	}
	return out.String()
}

// base32Decode reverses base32Encode. Input must already be upper-cased and
// contain only Crockford characters.
func base32Decode(s string) ([]byte, error) {
	out := make([]byte, 0, len(s)*5/8)
	var acc uint32
	var bits uint
	for i := 0; i < len(s); i++ {
		idx := strings.IndexByte(crockford, s[i])
		if idx < 0 {
			return nil, fmt.Errorf("%w: illegal character", ErrBadRecoveryKey)
		}
		acc = acc<<5 | uint32(idx)
		bits += 5
		if bits >= 8 {
			bits -= 8
			out = append(out, byte((acc>>bits)&0xff))
		}
	}
	// 21 payload bytes do not divide into 5-bit groups, so the final character
	// carries padding bits. base32Encode always writes them as zero; anything
	// else is a mistyped key. Without this check a typo confined to the padding
	// bits would decode to the same bytes and slip past the checksum.
	if bits > 0 && acc&((1<<bits)-1) != 0 {
		return nil, fmt.Errorf("%w: trailing bits (mistyped?)", ErrBadRecoveryKey)
	}
	return out, nil
}
