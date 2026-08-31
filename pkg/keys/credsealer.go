package keys

// Target-credential sealing (decisions.md #14).
//
// S3 target credentials are key-class material: stored only as TW-wrapped
// ciphertext, never returned by any read path, decrypted only in worker memory
// at run time. They reuse the very same versioned AEAD envelope as the DK wraps
// — one audited crypto path, no second implementation (AGENTS.md: never
// hand-roll cryptography).
//
// The TW key is a distinct 256-bit cluster/KMS secret from the SRW key so the
// two custody concerns rotate independently.

import "fmt"

// TWSealer seals and opens arbitrary secret payloads under the Target Wrap key.
// pkg/targets adapts this to its CredSealer interface, keeping the crypto here.
type TWSealer struct {
	key []byte
}

// NewTWSealer constructs a sealer around a 32-byte Target Wrap key. The key is
// copied so the caller may zeroize its own buffer. It is never logged or
// returned.
func NewTWSealer(twKey []byte) (*TWSealer, error) {
	if len(twKey) != SRWKeySize {
		return nil, fmt.Errorf("%w: TW key must be %d bytes", ErrBadEnvelope, SRWKeySize)
	}
	k := make([]byte, SRWKeySize)
	copy(k, twKey)
	return &TWSealer{key: k}, nil
}

// Seal wraps a plaintext payload, returning the envelope and its format version.
func (s *TWSealer) Seal(plaintext []byte) (blob []byte, version int, err error) {
	blob, err = seal(plaintext, s.key, WrapTW, kdfNone, ArgonParams{})
	if err != nil {
		return nil, 0, err
	}
	return blob, EnvelopeVersion, nil
}

// Open unwraps a previously sealed payload for immediate, in-memory use.
func (s *TWSealer) Open(blob []byte) ([]byte, error) {
	info, err := Inspect(blob)
	if err != nil {
		return nil, err
	}
	if info.Kind != WrapTW {
		return nil, fmt.Errorf("%w: not a TW envelope", ErrBadEnvelope)
	}
	return open(blob, s.key)
}

// Close zeroizes the held TW key.
func (s *TWSealer) Close() { Zeroize(s.key) }
