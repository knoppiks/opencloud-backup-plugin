// Backup key setup endpoints (phase-3 deliverable 2 & 3).
//
// Trust invariants enforced here (decisions.md, AGENTS.md):
//   - The plaintext Recovery Key NEVER crosses the network. The browser
//     generates the RK, generates the DK, wraps the DK under the RK, and sends
//     the RK-wrapped envelope plus the raw DK. The DK does cross the wire, once,
//     at setup, over TLS: the server has to see it to produce the SRW wrap that
//     unattended runs need (decisions.md #1 — this is not zero-knowledge). It is
//     held for the length of that request and zeroized.
//   - The server adds the SRW wrap so unattended runs are possible
//     (decisions.md #1) and persists both envelopes. It stores no plaintext.
//   - Key material never appears in a response, an error message, or a log line.
//     Responses carry status metadata only.
//   - Every space-scoped route checks CS3 membership server-side; a non-member
//     gets 403 and learns nothing about the space.
package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/keys"
)

// maxKeyRequestBytes caps setup payloads; envelopes are a few hundred bytes.
const maxKeyRequestBytes = 16 << 10

// setupRequest is the client's contribution to the key ceremony.
//
// The browser has already: generated the RK, generated the DK, and wrapped the
// DK under the RK (Argon2id + AEAD, same envelope format as the server). It
// sends the RK-wrapped envelope for storage, and the raw DK **only** so the
// server can add the SRW wrap that unattended runs need.
//
// Sending the DK is what decisions.md #1 explicitly accepts ("SRW is not pure
// zero-knowledge"): the server must be able to reconstruct the DK for scheduled
// runs. The RK itself is never sent, so the user's recovery path stays
// exclusively theirs.
type setupRequest struct {
	// WrappedDKRK is the base64 RK-wrapped DK envelope, produced client-side.
	WrappedDKRK string `json:"wrapped_dk_rk"`
	// DataKey is the base64 raw 256-bit DK, used server-side only to produce the
	// SRW wrap. It is zeroized immediately after wrapping and never stored.
	DataKey string `json:"data_key"`
}

// keyStatusResponse is the key-material-free setup summary.
type keyStatusResponse struct {
	SpaceID    string `json:"space_id"`
	Configured bool   `json:"configured"`
	HasRK      bool   `json:"has_recovery_wrap"`
	HasSRW     bool   `json:"has_server_wrap"`
	RKVersion  int    `json:"recovery_wrap_version,omitempty"`
	SRWVersion int    `json:"server_wrap_version,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

// recoveryBlobResponse carries the RK-wrapped DK envelope to a space member
// (phase-3 deliverable 3). This is ciphertext: without the user's Recovery Key
// it is useless, so serving it to members upholds decisions.md #7 (a shared
// space's RK is retrievable by any member) without weakening confidentiality.
type recoveryBlobResponse struct {
	SpaceID string `json:"space_id"`
	// Envelope is the base64 RK-wrapped DK blob. Ciphertext only.
	Envelope string `json:"envelope"`
	Version  int    `json:"version"`
	// KDF and Argon parameters let an offline tool derive the KEK. They are
	// public parameters, not secrets, and are also embedded in the envelope.
	KDF            string `json:"kdf"`
	ArgonTime      uint32 `json:"argon_time,omitempty"`
	ArgonMemoryKiB uint32 `json:"argon_memory_kib,omitempty"`
	ArgonLanes     uint8  `json:"argon_lanes,omitempty"`
}

// handleKeySetup completes the key ceremony for a space: it stores the
// client-produced RK envelope and adds the server's SRW wrap.
func (s *Server) handleKeySetup(w http.ResponseWriter, r *http.Request) {
	// Setup decides what every future snapshot of this Space is encrypted
	// under, so it takes the same authority OpenCloud requires to manage the
	// Space's membership.
	_, spaceID, ok := s.requireRole(w, r, cs3.RoleManager)
	if !ok {
		return
	}
	if s.keyStore == nil || s.srw == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "key service not configured")
		return
	}

	// Setting up a Space is a once-only act. A second ceremony would install a
	// new Data Key over the old one, and every snapshot already written would
	// stay encrypted under a key nobody holds any more — the backups would be
	// listed, intact, and permanently unreadable. There is no override: a Space
	// that needs a new Recovery Key rotates it (see handleRotateRecoveryKey),
	// and one that genuinely needs a new Data Key starts a new repository.
	if !s.assertNotConfigured(w, spaceID) {
		return
	}

	var req setupRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxKeyRequestBytes))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	rkBlob, ok := decodeRecoveryEnvelope(w, req.WrappedDKRK)
	if !ok {
		return
	}
	dk, err := base64.StdEncoding.DecodeString(req.DataKey)
	if err != nil || len(dk) != keys.DKSize {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid data key")
		return
	}
	// The DK is transient server-side: wrap it, then wipe it.
	defer keys.Zeroize(dk)

	info, ok := checkRecoveryEnvelope(w, rkBlob)
	if !ok {
		return
	}

	srwWrapped, err := s.srw.WrapSRW(dk)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not complete key setup")
		return
	}

	if err := s.keyStore.PutRK(spaceID, keys.WrappedDK{
		Version: info.Version, Kind: keys.WrapRK, Blob: rkBlob,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not store recovery envelope")
		return
	}
	if err := s.keyStore.PutSRW(spaceID, srwWrapped); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not store server envelope")
		return
	}

	st, err := s.keyStore.Status(spaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read key status")
		return
	}
	writeJSON(w, http.StatusCreated, toKeyStatusResponse(st))
}

// rotateRecoveryKeyRequest replaces a Space's recovery envelope, and nothing
// else. It carries no Data Key: the browser fetched the current envelope,
// unwrapped it with the old Recovery Key, generated a new one, and re-wrapped
// the *same* DK. Existing snapshots stay readable and nothing is re-uploaded.
//
// The server cannot verify that claim — checking it would need the Recovery Key,
// which never crosses the network — so the client invariant in
// key-envelope-format.md §5 is load-bearing: a client must unwrap the envelope
// it is about to POST, with the key it is about to show the user, and compare
// the DK against the one it just recovered. A client that gets this wrong leaves
// a Space whose status is green and whose new Recovery Key opens nothing. What
// this endpoint does guarantee is that the *old* envelope survives (R1 made
// envelopes append-only), so such a mistake is recoverable by an operator.
type rotateRecoveryKeyRequest struct {
	// WrappedDKRK is the base64 RK-wrapped DK envelope, produced client-side
	// under the new Recovery Key.
	WrappedDKRK string `json:"wrapped_dk_rk"`
}

// handleRotateRecoveryKey stores a new recovery envelope for a Space, leaving
// the Data Key — and therefore every existing backup — untouched.
func (s *Server) handleRotateRecoveryKey(w http.ResponseWriter, r *http.Request) {
	// Any member may *retrieve* the envelope (decisions.md #7), but replacing
	// it invalidates the Recovery Key every other member is holding, so
	// rotation sits with setup at the manager role.
	_, spaceID, ok := s.requireRole(w, r, cs3.RoleManager)
	if !ok {
		return
	}
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "key service not configured")
		return
	}
	// Rotation replaces something. A Space with no envelope has nothing to
	// rotate, and accepting one here would be a second way to run the ceremony
	// — the exact thing the setup guard exists to prevent.
	if !s.assertConfigured(w, spaceID) {
		return
	}

	var req rotateRecoveryKeyRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxKeyRequestBytes))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	rkBlob, ok := decodeRecoveryEnvelope(w, req.WrappedDKRK)
	if !ok {
		return
	}
	info, ok := checkRecoveryEnvelope(w, rkBlob)
	if !ok {
		return
	}

	// Append-only: the superseded envelope stays readable, which is what makes
	// a botched client-side rotation recoverable rather than terminal.
	if err := s.keyStore.PutRK(spaceID, keys.WrappedDK{
		Version: info.Version, Kind: keys.WrapRK, Blob: rkBlob,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not store recovery envelope")
		return
	}

	st, err := s.keyStore.Status(spaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read key status")
		return
	}
	writeJSON(w, http.StatusOK, toKeyStatusResponse(st))
}

// handleKeyStatus reports whether a space is set up. It returns no key material.
func (s *Server) handleKeyStatus(w http.ResponseWriter, r *http.Request) {
	_, spaceID, ok := s.requireRole(w, r, cs3.RoleViewer)
	if !ok {
		return
	}
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "key service not configured")
		return
	}

	st, err := s.keyStore.Status(spaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read key status")
		return
	}
	writeJSON(w, http.StatusOK, toKeyStatusResponse(st))
}

// handleRecoveryEnvelope serves the RK-wrapped DK blob to space members
// (decisions.md #7). The response is ciphertext plus public KDF parameters; the
// server neither holds nor learns the Recovery Key.
func (s *Server) handleRecoveryEnvelope(w http.ResponseWriter, r *http.Request) {
	_, spaceID, ok := s.requireRole(w, r, cs3.RoleViewer)
	if !ok {
		return
	}
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "key service not configured")
		return
	}

	wrapped, err := s.keyStore.GetRK(spaceID)
	if err != nil {
		var nf keys.ErrNotFound
		if errors.As(err, &nf) {
			writeError(w, http.StatusNotFound, "not_found", "backup not configured for this space")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read recovery envelope")
		return
	}
	info, err := keys.Inspect(wrapped.Blob)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read recovery envelope")
		return
	}

	writeJSON(w, http.StatusOK, recoveryBlobResponse{
		SpaceID:        spaceID,
		Envelope:       base64.StdEncoding.EncodeToString(wrapped.Blob),
		Version:        info.Version,
		KDF:            info.KDF,
		ArgonTime:      info.Argon.Time,
		ArgonMemoryKiB: info.Argon.MemoryKiB,
		ArgonLanes:     info.Argon.Lanes,
	})
}

// assertNotConfigured allows a request through only while the Space has no
// complete key setup. It writes 409 and returns false otherwise.
//
// The predicate is "both envelopes present", so a setup interrupted between the
// two writes can be finished by re-running it — that is a half-finished ceremony
// with no snapshots behind it, not a Data Key anything depends on.
func (s *Server) assertNotConfigured(w http.ResponseWriter, spaceID string) bool {
	st, ok := s.keyStatus(w, spaceID)
	if !ok {
		return false
	}
	if st.Configured {
		writeError(w, http.StatusConflict, "conflict",
			"backup keys already exist for this space; rotate the recovery key instead")
		return false
	}
	return true
}

// assertConfigured allows a request through only when the Space has a complete
// key setup. It writes 404 and returns false otherwise — the same answer a
// member gets for a Space that was never set up, because that is what it is.
func (s *Server) assertConfigured(w http.ResponseWriter, spaceID string) bool {
	st, ok := s.keyStatus(w, spaceID)
	if !ok {
		return false
	}
	if !st.Configured {
		writeError(w, http.StatusNotFound, "not_found", "backup not configured for this space")
		return false
	}
	return true
}

// keyStatus reads a Space's setup state, writing the error response itself when
// the store cannot answer. A store that fails here must not be read as "not
// configured": that would turn an outage into permission to overwrite a Data Key.
func (s *Server) keyStatus(w http.ResponseWriter, spaceID string) (keys.Status, bool) {
	st, err := s.keyStore.Status(spaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read key status")
		return keys.Status{}, false
	}
	return st, true
}

// decodeRecoveryEnvelope base64-decodes a client-supplied envelope.
func decodeRecoveryEnvelope(w http.ResponseWriter, encoded string) ([]byte, bool) {
	blob, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(blob) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid recovery envelope encoding")
		return nil, false
	}
	return blob, true
}

// checkRecoveryEnvelope applies the service's envelope policy to a
// client-produced blob: well-formed, an RK wrap, and derived with enough work to
// be worth storing. A weak envelope gets its own message because the client can
// fix it by redoing the ceremony with stronger parameters, whereas a malformed
// one is a bug.
func checkRecoveryEnvelope(w http.ResponseWriter, blob []byte) (keys.EnvelopeInfo, bool) {
	info, err := keys.CheckRecoveryEnvelope(blob)
	switch {
	case errors.Is(err, keys.ErrWeakEnvelope):
		writeError(w, http.StatusBadRequest, "bad_request",
			"recovery envelope key derivation is too weak")
		return keys.EnvelopeInfo{}, false
	case err != nil:
		writeError(w, http.StatusBadRequest, "bad_request", "invalid recovery envelope")
		return keys.EnvelopeInfo{}, false
	}
	return info, true
}

func toKeyStatusResponse(st keys.Status) keyStatusResponse {
	out := keyStatusResponse{
		SpaceID:    st.SpaceID,
		Configured: st.Configured,
		HasRK:      st.HasRK,
		HasSRW:     st.HasSRW,
		RKVersion:  st.RKVersion,
		SRWVersion: st.SRWVersion,
	}
	if !st.CreatedAt.IsZero() {
		out.CreatedAt = st.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if !st.UpdatedAt.IsZero() {
		out.UpdatedAt = st.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	return out
}

// srwWrapper is the subset of the SRW key holder the API needs. It is satisfied
// by *keys.SRWWrapper; the interface keeps the server testable without holding
// real key material in tests.
type srwWrapper interface {
	WrapSRW(dk []byte) (keys.WrappedDK, error)
}

// compile-time guard that the concrete wrapper satisfies the API's needs.
var _ srwWrapper = (*keys.SRWWrapper)(nil)
