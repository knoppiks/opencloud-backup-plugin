// Package takeout defines the Take-Out of restore Path A: its directory
// layout, its manifest, and the key-free integrity check both halves of the
// path run on it (decisions.md, "Restore paths (contract)").
//
// The split of powers is the whole point and is enforced by the package layout:
//
//   - The admin half, takeout/remote, runs with S3 access only. Extract has no
//     key parameter of any kind and therefore *cannot* decrypt: the admin moves
//     ciphertext, nothing else (decisions.md #2, #15).
//   - The user-side half lives in the subpackage takeout/decrypt, which runs on
//     the user's own machine with the Recovery Key, touches no network and needs
//     no OpenCloud (decisions.md, Path A). Keeping it out of the admin's
//     packages is what lets the admin's binary be built without any code that
//     unwraps a Data Key or restores a repository, rather than merely not
//     calling it.
//   - This package sits under both and links neither an S3 client nor an
//     unwrap path, so each side can read the format without the other's
//     capabilities (phase 10.6).
//
// A Take-Out is a plain directory so it stays readable by hand and by future
// tooling:
//
//	<out>/manifest.json     provenance + integrity records (no key material)
//	<out>/recovery.ocbke    the RK-wrapped Data Key envelope (ciphertext)
//	<out>/repo/...          the Space's kopia repository, copied verbatim
//
// This layout is a long-term promise in the same sense as the key envelope: the
// decrypt tool is the family's last resort and must keep reading old Take-Outs.
package takeout

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/knoppiks/opencloud-backup-plugin/internal/snapshot"
)

const (
	// ManifestFormat identifies a Take-Out directory.
	ManifestFormat = "opencloud-backup-takeout"
	// ManifestVersion is the current Take-Out manifest version. Bump additively;
	// the decrypt tool must keep reading every version.
	ManifestVersion = 1

	// ManifestFile, EnvelopeFile and RepoDir are the fixed names inside a
	// Take-Out directory.
	ManifestFile = "manifest.json"
	EnvelopeFile = "recovery.ocbke"
	RepoDir      = "repo"
)

// Errors surfaced to operators and users. They stay coarse on purpose: an
// unwrap failure must not hint at which part of the input was wrong, and no
// error ever carries key material.
var (
	// ErrNoTakeOut means the directory is not (or is no longer) a Take-Out.
	ErrNoTakeOut = errors.New("takeout: not a take-out directory (manifest.json missing)")
	// ErrNoEnvelope means the target holds no key envelope for the Space, so a
	// Take-Out from it could never be decrypted.
	ErrNoEnvelope = errors.New("takeout: no recovery key envelope found for this space")
	// ErrCorrupt means the Take-Out failed its integrity check.
	ErrCorrupt = errors.New("takeout: take-out is damaged")
	// ErrNewerTakeOut means the manifest declares a format version this build
	// does not know. The Take-Out is not damaged; a newer decrypt reads it.
	ErrNewerTakeOut = errors.New("takeout: take-out format is newer than this tool")
)

// SourceRef records where a Take-Out came from. It is provenance only and holds
// no credentials — a Take-Out is handed to an end user.
type SourceRef struct {
	Endpoint string `json:"endpoint,omitempty"`
	Bucket   string `json:"bucket,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
	// RepoPrefix and EnvelopeKey are the exact object keys that were copied.
	RepoPrefix  string `json:"repo_prefix"`
	EnvelopeKey string `json:"envelope_key"`
}

// EnvelopeRef describes the copied key envelope without revealing anything
// secret: format version, wrap kind and the (public) KDF parameters.
type EnvelopeRef struct {
	Version        int    `json:"version"`
	Kind           string `json:"kind"`
	KDF            string `json:"kdf"`
	ArgonTime      uint32 `json:"argon_time,omitempty"`
	ArgonMemoryKiB uint32 `json:"argon_memory_kib,omitempty"`
	ArgonLanes     uint8  `json:"argon_lanes,omitempty"`
}

// Manifest is the Take-Out's self-description. It contains no key material.
type Manifest struct {
	Format      string    `json:"format"`
	Version     int       `json:"version"`
	SpaceID     string    `json:"space_id"`
	CreatedAt   time.Time `json:"created_at"`
	Source      SourceRef `json:"source"`
	RepoDir     string    `json:"repo_dir"`
	EnvelopeRef string    `json:"envelope_file,omitempty"`
	// Envelope is absent when the Take-Out was forced without one.
	Envelope   *EnvelopeRef `json:"envelope,omitempty"`
	BlobCount  int          `json:"blob_count"`
	TotalBytes int64        `json:"total_bytes"`
	// Blobs records every copied repository blob (id, size, ciphertext digest)
	// so the copy can be verified offline.
	Blobs []snapshot.BlobRef `json:"blobs"`
}

// WriteManifest serializes the manifest into a Take-Out directory.
func WriteManifest(dir string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("takeout: encode manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), data, 0o600); err != nil {
		return fmt.Errorf("takeout: write manifest: %w", err)
	}
	return nil
}

// ReadManifest loads the manifest of a Take-Out directory.
func ReadManifest(dir string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Manifest{}, ErrNoTakeOut
		}
		return Manifest{}, fmt.Errorf("takeout: read manifest: %w", err)
	}
	return parseManifest(data)
}

// parseManifest decodes and checks a manifest's bytes. It is ReadManifest
// without the file system, so the parser can be fuzzed on its own.
func parseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("%w: manifest is unreadable", ErrCorrupt)
	}
	if m.Format != ManifestFormat {
		return Manifest{}, ErrNoTakeOut
	}
	if m.Version < 1 {
		return Manifest{}, fmt.Errorf("%w: manifest has no format version", ErrCorrupt)
	}
	if m.Version > ManifestVersion {
		return Manifest{}, fmt.Errorf("%w (version %d, this tool reads up to %d)",
			ErrNewerTakeOut, m.Version, ManifestVersion)
	}
	if m.SpaceID == "" {
		return Manifest{}, fmt.Errorf("%w: manifest names no space", ErrCorrupt)
	}
	if err := checkLocalPaths(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// checkLocalPaths refuses manifest paths that leave the Take-Out directory.
//
// A Take-Out travels from an administrator to a family member, often on a USB
// stick, and the manifest is just a file in it. A crafted "../" or absolute
// path would point decrypt at files outside the directory it was given
// (review-2026-10.md F4). Every path the manifest names must stay inside.
func checkLocalPaths(m Manifest) error {
	for _, p := range []struct{ field, value string }{
		{"repo_dir", m.RepoDir},
		{"envelope_file", m.EnvelopeRef},
	} {
		if p.value == "" {
			continue // the fixed default name applies
		}
		if !filepath.IsLocal(filepath.FromSlash(p.value)) {
			return fmt.Errorf("%w: manifest field %s points outside the take-out", ErrCorrupt, p.field)
		}
	}
	// Verify reads each recorded blob by its id from the repository
	// directory, so an id is a file name too. kopia's ids are flat names; one
	// with a separator or a dot-dot is not something a takeout wrote.
	for _, b := range m.Blobs {
		if b.ID == "" || b.ID == "." || strings.ContainsAny(b.ID, `/\`) || !filepath.IsLocal(b.ID) {
			return fmt.Errorf("%w: manifest lists a blob outside the repository", ErrCorrupt)
		}
	}
	return nil
}

// RepoPath returns the directory holding the copied repository.
func (m Manifest) RepoPath(dir string) string {
	sub := m.RepoDir
	if sub == "" {
		sub = RepoDir
	}
	return filepath.Join(dir, filepath.FromSlash(sub))
}

// EnvelopePath returns the file holding the RK-wrapped Data Key envelope.
func (m Manifest) EnvelopePath(dir string) string {
	name := m.EnvelopeRef
	if name == "" {
		name = EnvelopeFile
	}
	return filepath.Join(dir, filepath.FromSlash(name))
}
