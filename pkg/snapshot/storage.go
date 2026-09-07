package snapshot

// Blob-storage wiring for the repo-per-Space layout.
//
// Repo layout (documented convention — Phase 4 deliverable):
//
//	s3://<bucket>/<prefix>spaces/<space-id>/
//
// One kopia repository per Space (decisions.md #6), all Spaces sharing the
// target bucket beneath the deployment prefix. Everything below that key prefix
// is kopia-managed, encrypted, content-addressed blobs; no plaintext name or
// byte ever appears there (phase-0-findings.md Spike 2).

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/kopia/kopia/repo/blob"
	"github.com/kopia/kopia/repo/blob/filesystem"
	kopias3 "github.com/kopia/kopia/repo/blob/s3"
	"github.com/kopia/kopia/repo/blob/throttling"
)

const (
	// spacesSegment namespaces per-Space repositories inside a target's prefix.
	spacesSegment = "spaces"
	// keysSegment namespaces the per-Space key envelopes published alongside —
	// deliberately *not* inside — the repositories.
	keysSegment = "keys"
	// envelopeObject is the file name of a Space's RK-wrapped Data Key envelope.
	envelopeObject = "recovery.ocbke"
	// serverEnvelopeObject is the file name of a Space's SRW-wrapped Data Key
	// envelope, the service's own copy.
	serverEnvelopeObject = "server.ocbke"
)

// RepoPrefix returns the object-key prefix of one Space's repository, always
// with a trailing slash: "<prefix>spaces/<space-id>/".
func RepoPrefix(loc Location, ref SpaceRef) string {
	p := path.Join(strings.Trim(loc.Prefix, "/"), spacesSegment, ref.SpaceID)
	return strings.TrimPrefix(p, "/") + "/"
}

// EnvelopeKey returns the object key of a Space's RK-wrapped Data Key envelope:
// "<prefix>keys/<space-id>/recovery.ocbke".
//
// The envelope is published to the target so a Take-Out is self-contained and
// Path A works with OpenCloud fully down (decisions.md, restore-paths contract).
// It is ciphertext — useless without the user's Recovery Key — so it sits in the
// same trust class as the encrypted repository already stored there.
//
// It lives *outside* RepoPrefix on purpose: everything under a repository prefix
// is kopia-owned, and a foreign blob there could be reported as unknown or
// reclaimed by maintenance.
func EnvelopeKey(loc Location, ref SpaceRef) string {
	p := path.Join(strings.Trim(loc.Prefix, "/"), keysSegment, ref.SpaceID, envelopeObject)
	return strings.TrimPrefix(p, "/")
}

// ServerEnvelopeKey returns the object key of a Space's SRW-wrapped Data Key
// envelope: "<prefix>keys/<space-id>/server.ocbke".
//
// This is the service's own copy, published so that the state Space is no
// longer the only place it exists. Losing the state Space then costs a
// re-configuration instead of the ability to run unattended backups at all
// (decisions.md #16).
//
// It is ciphertext openable only with the cluster's SRW key. Publishing it does
// widen what an attacker holding *both* the target's contents and the SRW key
// can decrypt — that is a deliberate, recorded trust-model trade-off
// (decisions.md, trust & key model), taken because the SRW key already lives in
// the same cluster as the service account that reads plaintext Spaces.
//
// Like the recovery envelope it lives outside RepoPrefix: everything under a
// repository prefix is kopia-owned.
func ServerEnvelopeKey(loc Location, ref SpaceRef) string {
	p := path.Join(strings.Trim(loc.Prefix, "/"), keysSegment, ref.SpaceID, serverEnvelopeObject)
	return strings.TrimPrefix(p, "/")
}

// StorageOpener opens the blob storage backing a Space's repository. It is an
// interface so unit tests can run the full engine against a local filesystem
// backend, with no S3 and no container (AGENTS.md testability rule).
type StorageOpener interface {
	// Open returns the blob storage for repo. createIfMissing tells the driver
	// the caller intends to initialise a new repository there.
	Open(ctx context.Context, repo Repo, createIfMissing bool) (blob.Storage, error)
}

// S3Opener is the production StorageOpener: kopia's S3 driver pointed at the
// resolved target. Credentials come from the Repo's Location, i.e. from the
// TW-unwrapped target credentials held in worker memory (decisions.md #14).
type S3Opener struct {
	// Limits optionally caps bandwidth and concurrency against the target.
	Limits throttling.Limits
}

var _ StorageOpener = S3Opener{}

// Open builds the S3 blob storage for a Space's repository.
func (o S3Opener) Open(ctx context.Context, r Repo, createIfMissing bool) (blob.Storage, error) {
	loc := r.Location
	if loc.Bucket == "" {
		return nil, fmt.Errorf("snapshot: target bucket not configured")
	}
	if r.Space.SpaceID == "" {
		return nil, fmt.Errorf("snapshot: space id required")
	}

	st, err := kopias3.New(ctx, &kopias3.Options{
		BucketName: loc.Bucket,
		Prefix:     RepoPrefix(loc, r.Space),
		// kopia's S3 driver wants host:port without a scheme
		// (phase-0-findings.md Spike 2).
		Endpoint:        stripScheme(loc.Endpoint),
		DoNotUseTLS:     loc.DisableTLS,
		AccessKeyID:     loc.AccessKeyID,
		SecretAccessKey: loc.SecretAccessKey,
		Region:          loc.Region,
		Limits:          o.Limits,
	}, createIfMissing)
	if err != nil {
		// Never echo the endpoint's credentials; the driver error may mention
		// the bucket, which is not secret.
		return nil, fmt.Errorf("snapshot: open target storage: %w", err)
	}
	return st, nil
}

// FilesystemOpener stores repositories under a local directory using the same
// per-Space layout. It exists for unit tests and local development; production
// deployments always use S3Opener.
type FilesystemOpener struct {
	// Root is the directory that plays the role of the bucket.
	Root string
}

var _ StorageOpener = FilesystemOpener{}

// Open builds filesystem-backed blob storage for a Space's repository.
func (o FilesystemOpener) Open(ctx context.Context, r Repo, createIfMissing bool) (blob.Storage, error) {
	if o.Root == "" {
		return nil, fmt.Errorf("snapshot: filesystem opener root not configured")
	}
	if r.Space.SpaceID == "" {
		return nil, fmt.Errorf("snapshot: space id required")
	}

	dir := path.Join(o.Root, RepoPrefix(r.Location, r.Space))
	if createIfMissing {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("snapshot: create local storage dir: %w", err)
		}
	}

	st, err := filesystem.New(ctx, &filesystem.Options{Path: dir}, createIfMissing)
	if err != nil {
		return nil, fmt.Errorf("snapshot: open local storage: %w", err)
	}
	return st, nil
}

// DirOpener opens a repository that already sits at one exact directory, with
// no per-Space layout applied. It is what the offline decrypt CLI uses: a
// Take-Out has been copied out of S3 into a local folder, and the repository is
// simply *there* (Phase 5, Path A).
//
// Keeping it separate from FilesystemOpener keeps the last-resort recovery path
// as short as possible — no prefix arithmetic between the user and their data.
type DirOpener struct {
	// Dir is the directory holding the repository's blobs.
	Dir string
}

var _ StorageOpener = DirOpener{}

// Open builds filesystem-backed blob storage rooted at Dir. It never creates the
// directory: the offline path only ever reads an existing Take-Out.
func (o DirOpener) Open(ctx context.Context, _ Repo, createIfMissing bool) (blob.Storage, error) {
	if o.Dir == "" {
		return nil, fmt.Errorf("snapshot: repository directory not configured")
	}
	if createIfMissing {
		if err := os.MkdirAll(o.Dir, 0o700); err != nil {
			return nil, fmt.Errorf("snapshot: create local storage dir: %w", err)
		}
	}

	st, err := filesystem.New(ctx, &filesystem.Options{Path: o.Dir}, createIfMissing)
	if err != nil {
		return nil, fmt.Errorf("snapshot: open local storage: %w", err)
	}
	return st, nil
}

// stripScheme removes an http/https prefix; kopia's S3 driver expects a bare
// host:port.
func stripScheme(endpoint string) string {
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	return strings.TrimSuffix(endpoint, "/")
}
