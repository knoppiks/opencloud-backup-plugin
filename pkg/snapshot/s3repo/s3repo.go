// Package s3repo is the production snapshot.StorageOpener: kopia's S3 driver
// pointed at a resolved target.
//
// It lives outside package snapshot so that the offline decrypt tool, which
// only ever opens a repository from a local directory, is built without an S3
// client (phase 10.6). The service and the admin take-out tool import it; the
// recovery path must not.
package s3repo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kopia/kopia/repo/blob"
	kopias3 "github.com/kopia/kopia/repo/blob/s3"
	"github.com/kopia/kopia/repo/blob/throttling"

	"opencloud-backup-plugin/pkg/snapshot"
)

// ErrMissingCredentials means a target was opened without both halves of its
// static S3 credential.
var ErrMissingCredentials = errors.New(
	"snapshot: target S3 access key id and secret access key are both required")

// Opener opens a Space's repository on its S3 target. Credentials come from
// the Repo's Location, i.e. from the TW-unwrapped target credentials held in
// worker memory (decisions.md #14).
type Opener struct {
	// Limits optionally caps bandwidth and concurrency against the target.
	Limits throttling.Limits
}

var _ snapshot.StorageOpener = Opener{}

// Open builds the S3 blob storage for a Space's repository.
func (o Opener) Open(ctx context.Context, r snapshot.Repo, createIfMissing bool) (blob.Storage, error) {
	loc := r.Location
	if loc.Bucket == "" {
		return nil, fmt.Errorf("snapshot: target bucket not configured")
	}
	if r.Space.SpaceID == "" {
		return nil, fmt.Errorf("snapshot: space id required")
	}
	// kopia's driver falls back to AWS environment variables and the cloud
	// metadata service when handed empty credentials. A target with none is a
	// misconfiguration to report, not a reason to go looking elsewhere
	// (review-2026-10.md F5).
	if strings.TrimSpace(loc.AccessKeyID) == "" || strings.TrimSpace(loc.SecretAccessKey) == "" {
		return nil, ErrMissingCredentials
	}

	st, err := kopias3.New(ctx, &kopias3.Options{
		BucketName: loc.Bucket,
		Prefix:     snapshot.RepoPrefix(loc, r.Space),
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

// stripScheme removes an http/https prefix; kopia's S3 driver expects a bare
// host:port.
func stripScheme(endpoint string) string {
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	return strings.TrimSuffix(endpoint, "/")
}
