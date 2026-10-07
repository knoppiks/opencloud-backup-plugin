package objstore

// Observing, rather than assuming, whether a target can make an object
// undeletable (decisions.md #8, #9).
//
// The project's own target cannot: Garage implements neither S3 Object Lock nor
// bucket versioning, and its grants cannot express "may write, may not delete".
// That is recorded in decisions.md and pinned by a test, but a pinned fact about
// one version is not the same as knowing what the bucket in front of *this*
// deployment does — an operator may point the service at something else, and
// Garage may one day grow the feature. So the service asks.
//
// It only asks. There is no code here that turns a supported backend into an
// enforced retention, because there is no backend in this project's test
// environment that could exercise such code, and an immutability path that has
// never run against something that implements it is a claim rather than a
// feature. Issue #33 tracks closing that gap.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// ObjectLockSupport is what a probe was able to establish about a bucket's
// support for S3 Object Lock.
type ObjectLockSupport int

const (
	// ObjectLockUnknown means the probe could not tell — typically because the
	// credential it used may not read the bucket's configuration.
	ObjectLockUnknown ObjectLockSupport = iota
	// ObjectLockUnsupported means the backend answered that it does not
	// implement Object Lock at all.
	ObjectLockUnsupported
	// ObjectLockSupported means the backend implements Object Lock. It says
	// nothing about whether the bucket has it enabled; see Capabilities.
	ObjectLockSupported
)

// String renders the support level for logs.
func (s ObjectLockSupport) String() string {
	switch s {
	case ObjectLockUnsupported:
		return "unsupported"
	case ObjectLockSupported:
		return "supported"
	default:
		return "unknown"
	}
}

// Capabilities is what one probe observed about a bucket.
type Capabilities struct {
	// ObjectLock is whether the backend implements Object Lock.
	ObjectLock ObjectLockSupport
	// ObjectLockEnabled is true only when the bucket reports an active Object
	// Lock configuration. It is meaningful only when ObjectLock is
	// ObjectLockSupported.
	ObjectLockEnabled bool
	// VersioningEnabled reports the bucket's versioning status.
	//
	// False does not mean "the backend cannot do versioning". Garage answers
	// GetBucketVersioning successfully with an empty status — byte for byte what
	// a real S3 bucket that has never had versioning enabled returns — so the
	// two are indistinguishable without attempting to enable it, which is a
	// write this probe will not make against somebody's backup bucket. Pinned by
	// TestGarageCapabilitySignals.
	VersioningEnabled bool
}

// Immutable reports whether this bucket actually makes objects undeletable
// today. It is deliberately strict: only an enabled Object Lock configuration
// counts, because versioning alone does not stop a credential that may delete
// from deleting versions too.
func (c Capabilities) Immutable() bool {
	return c.ObjectLock == ObjectLockSupported && c.ObjectLockEnabled
}

// Prober observes a target's immutability capabilities. It is an interface
// because it is an S3 boundary: callers take it as a dependency so their tests
// do not reach for a network.
type Prober interface {
	Probe(ctx context.Context, cfg S3Config) (Capabilities, error)
}

// S3Prober is the real Prober.
type S3Prober struct {
	// Timeout bounds the probe. It is a diagnostic, so it must never be able to
	// spend a caller's whole budget; zero means DefaultProbeTimeout.
	Timeout time.Duration
}

// DefaultProbeTimeout bounds a capability probe against an unresponsive target.
const DefaultProbeTimeout = 30 * time.Second

var _ Prober = S3Prober{}

// Probe asks a bucket what it supports. It makes read-only calls and changes
// nothing.
//
// An error means the probe itself could not be run (the endpoint was
// unreachable, the client could not be built). A backend that answers "I do not
// implement this" is not an error — it is the answer.
func (p S3Prober) Probe(ctx context.Context, cfg S3Config) (Capabilities, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	store, err := NewS3(ctx, cfg)
	if err != nil {
		return Capabilities{}, fmt.Errorf("objstore: probe capabilities: %w", err)
	}
	return probeWith(ctx, store.client, cfg.Bucket), nil
}

// lockConfigReader is the part of the S3 client the probe uses, so the
// interpretation of each answer can be tested without a bucket.
type lockConfigReader interface {
	GetObjectLockConfiguration(ctx context.Context, in *s3.GetObjectLockConfigurationInput,
		opts ...func(*s3.Options)) (*s3.GetObjectLockConfigurationOutput, error)
	GetBucketVersioning(ctx context.Context, in *s3.GetBucketVersioningInput,
		opts ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error)
}

func probeWith(ctx context.Context, client lockConfigReader, bucket string) Capabilities {
	var caps Capabilities

	lock, err := client.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{
		Bucket: aws.String(bucket),
	})
	switch {
	case err == nil:
		caps.ObjectLock = ObjectLockSupported
		caps.ObjectLockEnabled = lock.ObjectLockConfiguration != nil &&
			lock.ObjectLockConfiguration.ObjectLockEnabled != ""
	case apiErrorCode(err) == "NotImplemented":
		caps.ObjectLock = ObjectLockUnsupported
	case apiErrorCode(err) == "ObjectLockConfigurationNotFoundError":
		// The backend implements Object Lock; this bucket was simply not
		// created with it enabled, which cannot be changed after the fact.
		caps.ObjectLock = ObjectLockSupported
	default:
		// AccessDenied, a network failure, an unknown code: the honest answer is
		// that we do not know, not that the feature is missing.
		caps.ObjectLock = ObjectLockUnknown
	}

	// The nil check is not paranoia about the SDK: this is a diagnostic that
	// runs inside a prune, and a probe that panics would take a maintenance run
	// with it.
	if versioning, err := client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{
		Bucket: aws.String(bucket),
	}); err == nil && versioning != nil {
		caps.VersioningEnabled = versioning.Status == types.BucketVersioningStatusEnabled
	}

	return caps
}

// apiErrorCode returns the S3 error code, or "" when err is not an API error.
func apiErrorCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return ""
}
