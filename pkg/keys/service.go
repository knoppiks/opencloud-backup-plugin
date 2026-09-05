package keys

// Data Key lifecycle: generation, wrapping under the Recovery Key (RK) and the
// Server Runtime Wrap key (SRW), unwrapping, and rotation.
//
// Invariants (decisions.md, AGENTS.md):
//   - The plaintext DK exists only in memory during a run; it is never persisted
//     or logged.
//   - The plaintext RK never crosses the network: WrapWithRK/UnwrapRK run
//     client-side (Phase 8) or in the offline decrypt CLI (Phase 5). They live
//     here because the CLI links this package — the server simply never calls
//     them with a real RK.
//   - Rotation re-wraps the same DK, so snapshot data is untouched.

import (
	"crypto/rand"
	"fmt"
	"time"
)

// SRWKeySize is the required length of the Server Runtime Wrap key, and of the
// Target Wrap key (both are 256-bit random cluster/KMS secrets).
const SRWKeySize = kekSize

// Clock supplies the current time, injected for deterministic tests.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// GenerateDK returns a fresh 256-bit Data Key from crypto/rand. The caller owns
// the buffer and should Zeroize it when done.
func GenerateDK() ([]byte, error) {
	dk := make([]byte, DKSize)
	if _, err := rand.Read(dk); err != nil {
		return nil, fmt.Errorf("keys: generate DK: %w", err)
	}
	return dk, nil
}

// WrapWithRK wraps a Data Key under a Recovery Key using Argon2id + AEAD.
//
// Runs client-side or in the offline CLI — the server never receives a plaintext
// RK. params allows raising Argon2id costs over time; pass DefaultArgonParams
// for the current baseline.
func WrapWithRK(dk, rk []byte, params ArgonParams) (WrappedDK, error) {
	if len(dk) != DKSize {
		return WrappedDK{}, fmt.Errorf("%w: DK must be %d bytes", ErrBadEnvelope, DKSize)
	}
	blob, err := seal(dk, rk, WrapRK, kdfArgon2id, params)
	if err != nil {
		return WrappedDK{}, err
	}
	return WrappedDK{
		Version:   EnvelopeVersion,
		Kind:      WrapRK,
		Blob:      blob,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// UnwrapRK recovers a Data Key from an RK envelope. Argon2id parameters come
// from the envelope itself, so envelopes written under older parameters keep
// working after a parameter bump.
func UnwrapRK(w WrappedDK, rk []byte) ([]byte, error) {
	if w.Kind != WrapRK {
		return nil, fmt.Errorf("%w: not an RK envelope", ErrBadEnvelope)
	}
	return open(w.Blob, rk)
}

// WrapWithSRW wraps a Data Key under the server-held SRW key. The SRW key is
// already uniformly random, so no KDF stretching is applied.
func WrapWithSRW(dk, srwKey []byte) (WrappedDK, error) {
	if len(dk) != DKSize {
		return WrappedDK{}, fmt.Errorf("%w: DK must be %d bytes", ErrBadEnvelope, DKSize)
	}
	if len(srwKey) != SRWKeySize {
		return WrappedDK{}, fmt.Errorf("%w: SRW key must be %d bytes", ErrBadEnvelope, SRWKeySize)
	}
	blob, err := seal(dk, srwKey, WrapSRW, kdfNone, ArgonParams{})
	if err != nil {
		return WrappedDK{}, err
	}
	return WrappedDK{
		Version:   EnvelopeVersion,
		Kind:      WrapSRW,
		Blob:      blob,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// UnwrapSRW recovers a Data Key from an SRW envelope using the server-held key.
// This is the unattended worker's path (decisions.md #1).
func UnwrapSRW(w WrappedDK, srwKey []byte) ([]byte, error) {
	if w.Kind != WrapSRW {
		return nil, fmt.Errorf("%w: not an SRW envelope", ErrBadEnvelope)
	}
	if len(srwKey) != SRWKeySize {
		return nil, fmt.Errorf("%w: SRW key must be %d bytes", ErrBadEnvelope, SRWKeySize)
	}
	return open(w.Blob, srwKey)
}

// RotateSRW re-wraps the DK under a new SRW key without touching snapshot data.
// The old envelope is superseded: it no longer opens under the new key, while
// the DK — and therefore every existing snapshot — is unchanged.
func RotateSRW(old WrappedDK, oldKey, newKey []byte) (WrappedDK, error) {
	dk, err := UnwrapSRW(old, oldKey)
	if err != nil {
		return WrappedDK{}, err
	}
	defer Zeroize(dk)
	return WrapWithSRW(dk, newKey)
}

// RotateRK re-wraps the DK under a new Recovery Key. Client-side / CLI only.
func RotateRK(old WrappedDK, oldRK, newRK []byte, params ArgonParams) (WrappedDK, error) {
	dk, err := UnwrapRK(old, oldRK)
	if err != nil {
		return WrappedDK{}, err
	}
	defer Zeroize(dk)
	return WrapWithRK(dk, newRK, params)
}

// SRWWrapper is the server-side Wrapper implementation: it holds the SRW key
// (from a K8s secret / KMS) and unwraps DKs for the unattended worker.
//
// The key is held in memory only; it is never logged, never returned, and never
// exposed through the API (decisions.md #1).
type SRWWrapper struct {
	key []byte
}

// NewSRWWrapper constructs a wrapper around a 32-byte SRW key. The wrapper
// copies the key so the caller may zeroize its own buffer.
func NewSRWWrapper(srwKey []byte) (*SRWWrapper, error) {
	if len(srwKey) != SRWKeySize {
		return nil, fmt.Errorf("%w: SRW key must be %d bytes", ErrBadEnvelope, SRWKeySize)
	}
	k := make([]byte, SRWKeySize)
	copy(k, srwKey)
	return &SRWWrapper{key: k}, nil
}

var _ Wrapper = (*SRWWrapper)(nil)

// UnwrapSRW implements Wrapper.
func (s *SRWWrapper) UnwrapSRW(w WrappedDK) ([]byte, error) {
	return UnwrapSRW(w, s.key)
}

// WrapSRW wraps a DK under the held SRW key (used at setup time).
func (s *SRWWrapper) WrapSRW(dk []byte) (WrappedDK, error) {
	return WrapWithSRW(dk, s.key)
}

// Close zeroizes the held key.
func (s *SRWWrapper) Close() {
	Zeroize(s.key)
}

// GenerateSRWKey returns a fresh 256-bit key suitable for SRW or TW custody.
// Operators generate this once and store it in a K8s secret / KMS.
func GenerateSRWKey() ([]byte, error) {
	k := make([]byte, SRWKeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("keys: generate SRW key: %w", err)
	}
	return k, nil
}
