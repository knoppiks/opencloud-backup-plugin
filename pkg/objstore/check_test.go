package objstore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// TestCheckClassifiesEachAnswer drives the mapping directly, because the
// mapping is the whole of this feature: every other line is plumbing around a
// ListObjectsV2 call.
func TestCheckClassifiesEachAnswer(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want CheckOutcome
	}{
		{"an empty bucket is still a working target", nil, CheckOK},

		{"deadline", context.DeadlineExceeded, CheckTimeout},
		{"deadline wrapped by the retryer", fmt.Errorf("operation error S3: %w",
			context.DeadlineExceeded), CheckTimeout},
		{"cancelled", context.Canceled, CheckTimeout},

		{"dial failure", errors.New("dial tcp 10.0.0.1:3900: connect: connection refused"),
			CheckUnreachable},
		{"tls failure", errors.New("x509: certificate signed by unknown authority"),
			CheckUnreachable},

		{"unknown key id", apiError("InvalidAccessKeyId"), CheckAuthFailed},
		{"bad signature", apiError("SignatureDoesNotMatch"), CheckAuthFailed},
		{"malformed authorization header", apiError("AuthorizationHeaderMalformed"),
			CheckAuthFailed},

		{"no such bucket", apiError("NoSuchBucket"), CheckBucketMissing},
		{"not found", apiError("NotFound"), CheckBucketMissing},

		{"access denied", apiError("AccessDenied"), CheckDenied},
		{"all access disabled", apiError("AllAccessDisabled"), CheckDenied},

		{"an S3 code nobody mapped", apiError("SlowDown"), CheckUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyCheck(tc.err); got != tc.want {
				t.Fatalf("classify(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// A timeout must not be readable as a credential problem. The retryer wraps a
// deadline in machinery that carries an API error code of its own, and an admin
// sent to re-type a correct secret key because their buddy's machine was asleep
// is a worse outcome than no check at all.
func TestTimeoutIsNotMistakenForAnAuthFailure(t *testing.T) {
	wrapped := fmt.Errorf("exceeded maximum number of attempts: %w",
		fmt.Errorf("%w: %w", apiError("RequestTimeout"), context.DeadlineExceeded))
	if got := classifyCheck(wrapped); got != CheckTimeout {
		t.Fatalf("classify = %q, want %q", got, CheckTimeout)
	}
}

// fakeLister records the request the check makes, so the read-only and
// bounded-size properties are asserted rather than assumed.
type fakeLister struct {
	in  *s3.ListObjectsV2Input
	err error
}

func (f *fakeLister) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input,
	_ ...func(*s3.Options),
) (*s3.ListObjectsV2Output, error) {
	f.in = in
	return &s3.ListObjectsV2Output{}, f.err
}

// The check must ask for one key under the target's own prefix. Listing a whole
// bucket to answer "can I reach it" would make the diagnostic's cost scale with
// somebody's backup history.
func TestCheckAsksForOneKeyUnderThePrefix(t *testing.T) {
	lister := &fakeLister{}
	if err := listOneObject(context.Background(), lister, "household-backups", "vault/"); err != nil {
		t.Fatalf("listOneObject: %v", err)
	}
	if got := aws.ToString(lister.in.Bucket); got != "household-backups" {
		t.Fatalf("bucket = %q", got)
	}
	if got := aws.ToString(lister.in.Prefix); got != "vault/" {
		t.Fatalf("prefix = %q", got)
	}
	if got := aws.ToInt32(lister.in.MaxKeys); got != 1 {
		t.Fatalf("max keys = %d, want 1", got)
	}
}

// A misconfiguration that cannot even build a client is not a verdict about the
// network, and must not be reported as one.
func TestCheckWithNoBucketIsUnknownRatherThanUnreachable(t *testing.T) {
	got := S3Checker{}.Check(context.Background(), S3Config{Endpoint: "example.invalid"}, "")
	if got != CheckUnknown {
		t.Fatalf("check without a bucket = %q, want %q", got, CheckUnknown)
	}
}

func TestCheckerFuncAdaptsAFunction(t *testing.T) {
	var c Checker = CheckerFunc(func(context.Context, S3Config, string) CheckOutcome {
		return CheckDenied
	})
	if got := c.Check(context.Background(), S3Config{}, ""); got != CheckDenied {
		t.Fatalf("CheckerFunc = %q", got)
	}
}
