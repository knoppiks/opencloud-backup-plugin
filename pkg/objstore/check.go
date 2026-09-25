package objstore

// Telling an admin whether a target they just typed in actually works, without
// telling them anything else.
//
// This backs the admin API's connection check (phase-8-web-ui.md, sub-phase
// 8b). Two properties shape it, and both are constraints rather than taste:
//
//   - It reads and never writes. The bucket it is pointed at may be somebody's
//     live backup store, and a diagnostic has no business leaving anything in
//     it.
//   - It answers a classification and nothing else. The caller supplies the
//     endpoint, so the check is an outbound request to a host of their
//     choosing and the result is a channel back to them. Returning the
//     upstream error would turn "is my bucket reachable" into a probe of
//     whatever else answers on the household's network, with the reply quoted
//     verbatim. Six named outcomes carry everything an admin can act on.

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// CheckOutcome is the coarse verdict of a reachability check.
type CheckOutcome string

const (
	// CheckOK means the credentials listed the bucket.
	CheckOK CheckOutcome = "ok"
	// CheckUnreachable means nothing answered: DNS, connection refused, TLS.
	CheckUnreachable CheckOutcome = "unreachable"
	// CheckTimeout means something answered too slowly, or not at all in time.
	CheckTimeout CheckOutcome = "timeout"
	// CheckAuthFailed means the credentials were rejected as credentials — an
	// unknown key id or a bad signature. Typically a typo in the secret.
	CheckAuthFailed CheckOutcome = "auth_failed"
	// CheckDenied means the credentials are valid but may not read this bucket.
	CheckDenied CheckOutcome = "denied"
	// CheckBucketMissing means the endpoint has no such bucket.
	CheckBucketMissing CheckOutcome = "bucket_missing"
	// CheckUnknown means the endpoint answered something this code does not
	// recognise. The honest answer is that we do not know, exactly as with an
	// unreadable Object Lock configuration — inventing a nicer verdict would
	// send an admin looking in the wrong place.
	CheckUnknown CheckOutcome = "unknown"
)

// Checker reports whether a target is usable with the given configuration. It
// is an interface for the same reason Prober is: it is an S3 boundary, and
// callers take it as a dependency so their tests do not reach for a network.
type Checker interface {
	Check(ctx context.Context, cfg S3Config, prefix string) CheckOutcome
}

// CheckerFunc adapts a function to Checker.
type CheckerFunc func(ctx context.Context, cfg S3Config, prefix string) CheckOutcome

// Check implements Checker.
func (f CheckerFunc) Check(ctx context.Context, cfg S3Config, prefix string) CheckOutcome {
	return f(ctx, cfg, prefix)
}

// DefaultCheckTimeout bounds a check against an unresponsive endpoint. It is
// short because a human is waiting on the answer.
const DefaultCheckTimeout = 15 * time.Second

// S3Checker is the real Checker.
type S3Checker struct {
	// Timeout bounds the check; zero means DefaultCheckTimeout.
	Timeout time.Duration
}

var _ Checker = S3Checker{}

// Check lists at most one object under prefix and classifies what happened. It
// changes nothing in the bucket.
func (c S3Checker) Check(ctx context.Context, cfg S3Config, prefix string) CheckOutcome {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	store, err := NewS3(ctx, cfg)
	if err != nil {
		// The configuration could not even be assembled — a missing bucket
		// name, or an unusable ambient AWS config. Not a network verdict.
		return CheckUnknown
	}
	return classifyCheck(listOneObject(ctx, store.client, cfg.Bucket, prefix))
}

// objectLister is the part of the S3 client the check uses, so every verdict
// below can be tested without a bucket.
type objectLister interface {
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input,
		opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// listOneObject asks for a single key. An empty bucket is a perfectly good
// answer: the question is whether the credentials may read it, not whether a
// backup has run yet.
func listOneObject(ctx context.Context, client objectLister, bucket, prefix string) error {
	_, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(1),
	})
	return err
}

// classifyCheck maps one S3 error onto an outcome. The mapping is the whole
// point of the file, so it is a function a test can drive directly.
func classifyCheck(err error) CheckOutcome {
	if err == nil {
		return CheckOK
	}
	// Deadline first: a timeout surfaces wrapped in retry machinery, and
	// several of the codes below could otherwise swallow it.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return CheckTimeout
	}

	switch apiErrorCode(err) {
	case "":
		// No S3 error code at all: nothing answered in S3's language.
		return CheckUnreachable
	case "InvalidAccessKeyId", "SignatureDoesNotMatch", "InvalidSecurity",
		"AuthorizationHeaderMalformed", "InvalidAccessKeyID":
		return CheckAuthFailed
	case "NoSuchBucket", "NotFound":
		return CheckBucketMissing
	case "AccessDenied", "AllAccessDisabled", "Forbidden":
		return CheckDenied
	default:
		return CheckUnknown
	}
}
