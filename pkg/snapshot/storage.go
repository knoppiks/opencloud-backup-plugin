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

// spacesSegment namespaces per-Space repositories inside a target's prefix.
const spacesSegment = "spaces"

// RepoPrefix returns the object-key prefix of one Space's repository, always
// with a trailing slash: "<prefix>spaces/<space-id>/".
func RepoPrefix(loc Location, ref SpaceRef) string {
	p := path.Join(strings.Trim(loc.Prefix, "/"), spacesSegment, ref.SpaceID)
	return strings.TrimPrefix(p, "/") + "/"
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

// stripScheme removes an http/https prefix; kopia's S3 driver expects a bare
// host:port.
func stripScheme(endpoint string) string {
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	return strings.TrimSuffix(endpoint, "/")
}
