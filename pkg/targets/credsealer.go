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

// credPair mirrors PlainCreds field-for-field, so a direct conversion is
// possible and adding a credential field breaks compilation here until it is
// handled.
type credPair struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

// credPayload is the serialized plaintext form inside the envelope. It is
// versioned implicitly by the envelope's own version field.
//
// The backup pair is embedded, so it serialises flat — exactly the shape every
// record written before roles existed has, and those records must keep opening
// unchanged. The maintenance pair is a nested object, omitted when absent, so an
// older record decodes to "no maintenance credential", which is the truth about
// it. Nothing has to be migrated or re-sealed.
type credPayload struct {
	credPair
	Maintenance *credPair `json:"maintenance,omitempty"`
}

// Seal wraps a target's credentials for storage.
func (e *envelopeSealer) Seal(c CredentialSet) ([]byte, int, error) {
	if !c.Backup.Complete() {
		return nil, 0, fmt.Errorf("targets: incomplete credentials")
	}
	// A half-filled maintenance pair is refused rather than ignored: ignoring it
	// silently falls back to the backup credential, so a typo in one of the two
	// variables would produce a deployment that looks separated and is not.
	if !c.Maintenance.Empty() && !c.Maintenance.Complete() {
		return nil, 0, fmt.Errorf("targets: incomplete maintenance credentials")
	}

	payload := credPayload{credPair: credPair(c.Backup)}
	if c.Maintenance.Complete() {
		pair := credPair(c.Maintenance)
		payload.Maintenance = &pair
	}

	pt, err := json.Marshal(payload)
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
func (e *envelopeSealer) Open(wrapped []byte) (CredentialSet, error) {
	pt, err := e.sealer.Open(wrapped)
	if err != nil {
		// Never distinguish wrong-key from tampered, never echo the blob.
		return CredentialSet{}, fmt.Errorf("targets: cannot open credentials")
	}
	defer keys.Zeroize(pt)

	var p credPayload
	if err := json.Unmarshal(pt, &p); err != nil {
		return CredentialSet{}, fmt.Errorf("targets: cannot decode credentials")
	}

	out := CredentialSet{Backup: PlainCreds(p.credPair)}
	if p.Maintenance != nil {
		out.Maintenance = PlainCreds(*p.Maintenance)
	}
	return out, nil
}
