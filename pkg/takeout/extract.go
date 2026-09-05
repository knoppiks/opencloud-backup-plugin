package takeout

// The admin Take-Out (Path A, step 1). It copies a Space's ciphertext out of
// the S3 target and nothing else:
//
//   - No OpenCloud dependency — it must work with the whole deployment down.
//   - No key input. ExtractOptions carries no key, passphrase, or envelope
//     secret, so the tool is structurally incapable of decrypting what it
//     copies (decisions.md #2, #15). Do not add one.
//
// It copies the whole repository rather than filtering to a single snapshot:
// full-repo copy is always correct and needs no understanding of kopia's
// internals, which is exactly the property a disaster-recovery tool wants
// (phase-5 plan, "start with full-repo copy").

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/objstore"
	"opencloud-backup-plugin/pkg/snapshot"
)

// ExtractOptions configures a Take-Out.
//
// There is deliberately no field here that could carry key material.
type ExtractOptions struct {
	// Repos opens the Space's repository on the target. It is only ever used to
	// read and copy blobs; the repository itself is never opened, so no Data Key
	// is needed or possible.
	Repos snapshot.StorageOpener
	// Objects reads the published key envelope from the same target.
	Objects objstore.Store
	// Location addresses the target. Its credentials are used to read ciphertext
	// and are never written to the manifest.
	Location snapshot.Location
	// SpaceID selects the Space whose repository is extracted.
	SpaceID string
	// OutDir is the Take-Out directory; it is created if missing.
	OutDir string
	// AllowMissingEnvelope proceeds even when the target holds no key envelope
	// for the Space. The result is then *not* decryptable on its own, so this is
	// opt-in and recorded (by omission) in the manifest.
	AllowMissingEnvelope bool
	// Clock is injected for deterministic tests.
	Clock func() time.Time
	// Logger receives progress: counts and the space id only.
	Logger *slog.Logger
}

// Extract copies a Space's repository and key envelope out of the target into a
// self-contained Take-Out directory, and returns the manifest it wrote.
func Extract(ctx context.Context, opts ExtractOptions) (Manifest, error) {
	if opts.Repos == nil {
		return Manifest{}, fmt.Errorf("takeout: repository opener is required")
	}
	if opts.SpaceID == "" {
		return Manifest{}, fmt.Errorf("takeout: space id is required")
	}
	if opts.OutDir == "" {
		return Manifest{}, fmt.Errorf("takeout: output directory is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := time.Now
	if opts.Clock != nil {
		now = opts.Clock
	}

	ref := snapshot.SpaceRef{SpaceID: opts.SpaceID}
	redacted := opts.Location.Redacted()

	envelope, err := readEnvelope(ctx, opts.Objects, snapshot.EnvelopeKey(opts.Location, ref), opts.AllowMissingEnvelope)
	if err != nil {
		return Manifest{}, err
	}

	if err := os.MkdirAll(opts.OutDir, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("takeout: create output directory: %w", err)
	}

	src, err := opts.Repos.Open(ctx, snapshot.Repo{Location: opts.Location, Space: ref}, false)
	if err != nil {
		return Manifest{}, fmt.Errorf("takeout: open target repository: %w", err)
	}
	defer func() { _ = src.Close(context.WithoutCancel(ctx)) }()

	blobs, err := snapshot.CopyRepo(ctx, src, filepath.Join(opts.OutDir, RepoDir),
		func(count int, bytes int64) {
			if count%500 == 0 {
				logger.Info("take-out progress", "blobs", count, "bytes", bytes)
			}
		})
	if err != nil {
		return Manifest{}, fmt.Errorf("takeout: copy repository: %w", err)
	}

	manifest := Manifest{
		Format:    ManifestFormat,
		Version:   ManifestVersion,
		SpaceID:   opts.SpaceID,
		CreatedAt: now().UTC(),
		Source: SourceRef{
			Endpoint:    redacted.Endpoint,
			Bucket:      redacted.Bucket,
			Prefix:      redacted.Prefix,
			RepoPrefix:  snapshot.RepoPrefix(opts.Location, ref),
			EnvelopeKey: snapshot.EnvelopeKey(opts.Location, ref),
		},
		RepoDir: RepoDir,
		Blobs:   blobs,
	}
	for _, b := range blobs {
		manifest.TotalBytes += b.Size
	}
	manifest.BlobCount = len(blobs)

	if envelope != nil {
		if err := os.WriteFile(filepath.Join(opts.OutDir, EnvelopeFile), envelope.blob, 0o600); err != nil {
			return Manifest{}, fmt.Errorf("takeout: write key envelope: %w", err)
		}
		manifest.EnvelopeRef = EnvelopeFile
		manifest.Envelope = &envelope.ref
	} else {
		logger.Warn("no key envelope found; this take-out cannot be decrypted on its own",
			"space", opts.SpaceID)
	}

	if err := WriteManifest(opts.OutDir, manifest); err != nil {
		return Manifest{}, err
	}

	logger.Info("take-out complete",
		"space", opts.SpaceID, "blobs", manifest.BlobCount, "bytes", manifest.TotalBytes)
	return manifest, nil
}

// envelopeCopy is the fetched envelope plus its key-material-free description.
type envelopeCopy struct {
	blob []byte
	ref  EnvelopeRef
}

// readEnvelope fetches and validates the Space's RK-wrapped Data Key envelope.
// It only ever parses the envelope *header* — there is no key here to open it
// with, by design.
func readEnvelope(ctx context.Context, src objstore.Store, key string, allowMissing bool) (*envelopeCopy, error) {
	if src == nil {
		if allowMissing {
			return nil, nil
		}
		return nil, fmt.Errorf("takeout: object store is required to fetch the key envelope")
	}

	rc, err := src.Get(ctx, key)
	if err != nil {
		if errors.Is(err, objstore.ErrNotFound) {
			if allowMissing {
				return nil, nil
			}
			return nil, ErrNoEnvelope
		}
		return nil, err
	}
	defer func() { _ = rc.Close() }()

	blob, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("takeout: read key envelope: %w", err)
	}

	info, err := keys.Inspect(blob)
	if err != nil {
		return nil, fmt.Errorf("%w: key envelope is unreadable", ErrCorrupt)
	}
	if info.Kind != keys.WrapRK {
		return nil, fmt.Errorf("%w: stored envelope is not a recovery-key envelope", ErrCorrupt)
	}

	return &envelopeCopy{
		blob: blob,
		ref: EnvelopeRef{
			Version:        info.Version,
			Kind:           info.Kind.String(),
			KDF:            info.KDF,
			ArgonTime:      info.Argon.Time,
			ArgonMemoryKiB: info.Argon.MemoryKiB,
			ArgonLanes:     info.Argon.Lanes,
		},
	}, nil
}

// Verify re-reads a Take-Out and checks it against its own manifest. It needs no
// key: the integrity of the ciphertext is verifiable by whoever holds the copy.
func Verify(ctx context.Context, dir string) error {
	m, err := ReadManifest(dir)
	if err != nil {
		return err
	}

	if err := snapshot.VerifyRepoDir(ctx, repoPath(dir, m), m.Blobs); err != nil {
		return fmt.Errorf("%w: %s", ErrCorrupt, err.Error())
	}
	if m.EnvelopeRef != "" {
		if _, err := os.Stat(envelopePath(dir, m)); err != nil {
			return fmt.Errorf("%w: key envelope is missing", ErrCorrupt)
		}
	}
	return nil
}
