// Package keys implements the key service: the Data Key (DK), Recovery Key (RK),
// and Server Runtime Wrap (SRW) model from decisions.md ("Trust & key model").
//
// Hard rules (decisions.md, AGENTS.md):
//   - Never log key material (DK, RK, SRW blobs) or expose it in the admin UI.
//   - The plaintext RK is generated/used client-side only and never crosses the
//     network.
//   - The envelope format is a versioned, long-term compatibility promise; the
//     standalone decrypt CLI must parse it forever (see envelope.go for the
//     wire format specification).
//
// We do not hand-roll cryptography: the envelope uses XChaCha20-Poly1305 and
// Argon2id from golang.org/x/crypto. Data crypto and dedup remain kopia's job
// (decisions.md #5).
package keys

import "time"

// EnvelopeVersion is the current key-envelope format version. It is part of a
// long-term compatibility promise (decisions.md): bump only additively and keep
// the decrypt CLI able to parse every historical version.
const EnvelopeVersion = 1

// DKSize is the Data Key length in bytes (256-bit, decisions.md).
const DKSize = 32

// WrappedDK is a Data Key wrapped for storage. The plaintext DK never appears
// in a persisted WrappedDK. Which KEK wrapped it is recorded by Kind.
//
// Blob is the fully self-contained serialized envelope (magic, version, kind,
// KDF parameters, salt, nonce, ciphertext) — see envelope.go. Keeping the
// envelope self-describing is what lets the offline decrypt CLI (Phase 5) parse
// a blob with nothing but the RK.
type WrappedDK struct {
	// Version is the envelope format version (see EnvelopeVersion).
	Version int `json:"version"`
	// Kind distinguishes an RK-wrapped envelope from an SRW-wrapped one.
	Kind WrapKind `json:"kind"`
	// Blob is the opaque wrapped key material. Never logged.
	Blob []byte `json:"blob,omitempty"`
	// CreatedAt records when this envelope was produced. Metadata only.
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// WrapKind records which key wrapped a DK.
type WrapKind int

const (
	// WrapUnknown is the zero value and must not be persisted.
	WrapUnknown WrapKind = iota
	// WrapRK is a DK wrapped by the user's Recovery Key (client-side).
	WrapRK
	// WrapSRW is a DK wrapped by the Server Runtime Wrap key (cluster secret/KMS).
	WrapSRW
	// WrapTW is a credential blob wrapped by the Target Wrap key
	// (decisions.md #14). It shares this envelope primitive but never wraps a DK.
	WrapTW
)

// String renders the wrap kind for diagnostics. It never includes key material.
func (k WrapKind) String() string {
	switch k {
	case WrapRK:
		return "rk"
	case WrapSRW:
		return "srw"
	case WrapTW:
		return "tw"
	default:
		return "unknown"
	}
}

// SpaceKeys is the pre-versioned per-Space key record: both envelopes in one
// document. Since the R1 remediation each envelope is stored on its own, under
// keyenvelopes/<space-id>/{rk,srw}/<nanos>; this type remains because those
// older documents are still read (and never rewritten).
//
// It holds only wrapped material. The Argon2id parameters live inside the RK
// envelope itself, so the record stays simple and the envelope self-contained.
type SpaceKeys struct {
	SpaceID   string    `json:"space_id"`
	RK        WrappedDK `json:"rk,omitzero"`
	SRW       WrappedDK `json:"srw,omitzero"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Store persists and retrieves wrapped Data Keys per Space. It never stores or
// returns plaintext key material.
type Store interface {
	// GetSRW returns the SRW-wrapped DK for a space, or ErrNotFound.
	GetSRW(spaceID string) (WrappedDK, error)
	// PutSRW stores the SRW-wrapped DK for a space.
	PutSRW(spaceID string, w WrappedDK) error
	// GetRK returns the RK-wrapped DK for a space (for Take-Out export), or
	// ErrNotFound.
	GetRK(spaceID string) (WrappedDK, error)
	// PutRK stores the RK-wrapped DK for a space.
	PutRK(spaceID string, w WrappedDK) error
	// Status reports whether a space is configured and with which envelope
	// versions. It returns no key material.
	Status(spaceID string) (Status, error)
	// Spaces returns the ids of every space holding an envelope, in order. It
	// exists for the operations that must visit all of them — rotating the SRW
	// key, above all — and returns ids only, never envelopes.
	Spaces() ([]string, error)
}

// Status is the key-material-free summary of a space's backup key setup. It is
// what GET /keystatus serves (phase-3 deliverable 2): configured? created when?
// which wrap versions?
type Status struct {
	SpaceID string
	// Configured reports whether both wraps exist for the space.
	Configured bool
	// HasRK / HasSRW allow the UI to detect a half-finished setup.
	HasRK  bool
	HasSRW bool
	// RKVersion / SRWVersion are the envelope format versions in use.
	RKVersion  int
	SRWVersion int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Wrapper unwraps a Data Key for use inside a single run. The plaintext DK it
// returns lives only in memory and must never be logged or persisted.
type Wrapper interface {
	// UnwrapSRW recovers the plaintext DK from an SRW envelope using the
	// server-held SRW key. Used by the unattended worker.
	UnwrapSRW(w WrappedDK) (dk []byte, err error)
}

// ErrNotFound is returned by Store when no envelope exists for a space.
type ErrNotFound struct{ SpaceID string }

func (e ErrNotFound) Error() string { return "keys: no envelope for space " + e.SpaceID }
