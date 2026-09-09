// Package s3target holds the S3 (Garage) target configuration and a capability
// probe. The primary target is self-hosted Garage, which as of the pinned
// version has no S3 Object Lock and no bucket versioning (decisions.md #8;
// confirmed by phase-0-findings.md Spike 1).
//
// Any real S3 client built here must set the AWS SDK v2 checksum options to
// WhenRequired; the SDK default (WhenSupported) breaks multipart GET on Garage
// (phase-0-findings.md Spike 1, gotcha 4).
//
// NOTHING IMPLEMENTS ANY OF THIS YET, and no package outside this one imports
// it. The live S3 configuration types are snapshot.Location and
// objstore.S3Config; what is reserved here is the capability/probe boundary for
// the immutability work (decisions.md #9, Tier 2/3), which is unbuilt.
package s3target

import "context"

// Config describes how to reach the S3 target. Credentials are supplied out of
// band (cluster secret) and are never logged.
type Config struct {
	// Endpoint is host:port or a URL for the S3 API.
	Endpoint string
	// Region the target advertises (Garage uses "garage").
	Region string
	// Bucket is the target bucket.
	Bucket string
	// AccessKeyID / SecretAccessKey are the S3 credentials. Never logged.
	AccessKeyID     string
	SecretAccessKey string
	// UsePathStyle forces path-style addressing (required for Garage).
	UsePathStyle bool
	// DisableTLS talks plain HTTP (in-cluster Garage).
	DisableTLS bool
}

// Capabilities records what the target actually supports, discovered by Probe.
// Object Lock / versioning drive whether the kopia Object-Lock path can be
// enabled (decisions.md #5, #8); both are false on Garage today.
type Capabilities struct {
	ObjectLock       bool
	BucketVersioning bool
}

// Prober probes an S3 target for optional capabilities so higher layers can
// capability-flag immutability features (Phase 7).
type Prober interface {
	Probe(ctx context.Context, cfg Config) (Capabilities, error)
}
