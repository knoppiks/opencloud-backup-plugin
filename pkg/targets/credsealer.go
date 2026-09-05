package targets

// CredSealer implementation backed by the shared key-envelope primitive in
// pkg/keys (decisions.md #14: "The wrap uses the same maintained AEAD-envelope
// primitive as the DK wraps; no hand-rolled crypto").
//
// Plaintext credentials exist only transiently: inbound on an admin write, or
// in worker memory after Open. They are never persisted in the clear and never
// logged.

import (
	"encoding/json"
	"fmt"

	"opencloud-backup-plugin/pkg/keys"
)

// envelopeSealer adapts keys.TWSealer to the CredSealer interface.
type envelopeSealer struct {
	sealer *keys.TWSealer
}

// NewCredSealer builds a CredSealer from the 32-byte cluster/KMS Target Wrap
// key. The key is copied; the caller may zeroize its own buffer.
func NewCredSealer(twKey []byte) (CredSealer, error) {
	s, err := keys.NewTWSealer(twKey)
	if err != nil {
		return nil, fmt.Errorf("targets: cred sealer: %w", err)
	}
	return &envelopeSealer{sealer: s}, nil
}

var _ CredSealer = (*envelopeSealer)(nil)

// credPayload is the serialized plaintext form inside the envelope. It is
// versioned implicitly by the envelope's own version field.
type credPayload struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

// Seal wraps plaintext credentials for storage.
func (e *envelopeSealer) Seal(c PlainCreds) ([]byte, int, error) {
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return nil, 0, fmt.Errorf("targets: incomplete credentials")
	}
	// Direct conversion: credPayload mirrors PlainCreds field-for-field, so
	// adding a credential field breaks compilation here until it is handled.
	pt, err := json.Marshal(credPayload(c))
	if err != nil {
		// Deliberately generic: never echo credential content.
		return nil, 0, fmt.Errorf("targets: cannot encode credentials")
	}
	defer keys.Zeroize(pt)

	blob, version, err := e.sealer.Seal(pt)
	if err != nil {
		return nil, 0, fmt.Errorf("targets: cannot seal credentials")
	}
	return blob, version, nil
}

// Open unwraps stored credentials for immediate, in-memory use.
func (e *envelopeSealer) Open(wrapped []byte) (PlainCreds, error) {
	pt, err := e.sealer.Open(wrapped)
	if err != nil {
		// Never distinguish wrong-key from tampered, never echo the blob.
		return PlainCreds{}, fmt.Errorf("targets: cannot open credentials")
	}
	defer keys.Zeroize(pt)

	var p credPayload
	if err := json.Unmarshal(pt, &p); err != nil {
		return PlainCreds{}, fmt.Errorf("targets: cannot decode credentials")
	}
	return PlainCreds(p), nil
}
