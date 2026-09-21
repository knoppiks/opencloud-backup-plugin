package objstore

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// fakeBucket answers the two configuration reads a probe makes.
type fakeBucket struct {
	lockOut *s3.GetObjectLockConfigurationOutput
	lockErr error
	verOut  *s3.GetBucketVersioningOutput
	verErr  error
}

func (f fakeBucket) GetObjectLockConfiguration(context.Context, *s3.GetObjectLockConfigurationInput,
	...func(*s3.Options),
) (*s3.GetObjectLockConfigurationOutput, error) {
	return f.lockOut, f.lockErr
}

func (f fakeBucket) GetBucketVersioning(context.Context, *s3.GetBucketVersioningInput,
	...func(*s3.Options),
) (*s3.GetBucketVersioningOutput, error) {
	return f.verOut, f.verErr
}

func apiError(code string) error {
	return &smithy.GenericAPIError{Code: code, Message: code}
}

func TestProbeInterpretsEachAnswer(t *testing.T) {
	enabled := &s3.GetObjectLockConfigurationOutput{
		ObjectLockConfiguration: &types.ObjectLockConfiguration{
			ObjectLockEnabled: types.ObjectLockEnabledEnabled,
		},
	}

	cases := []struct {
		name          string
		bucket        fakeBucket
		wantLock      ObjectLockSupport
		wantEnabled   bool
		wantVersioned bool
		wantImmutable bool
	}{
		{
			// Garage's answer, pinned by TestGarageCapabilitySignals.
			name:     "backend does not implement object lock",
			bucket:   fakeBucket{lockErr: apiError("NotImplemented")},
			wantLock: ObjectLockUnsupported,
		},
		{
			// A real S3 bucket created without object lock. The feature exists;
			// this bucket cannot use it, which is a different thing and worth
			// telling an operator apart from the line above.
			name:     "implemented but not enabled on this bucket",
			bucket:   fakeBucket{lockErr: apiError("ObjectLockConfigurationNotFoundError")},
			wantLock: ObjectLockSupported,
		},
		{
			name:          "enabled",
			bucket:        fakeBucket{lockOut: enabled},
			wantLock:      ObjectLockSupported,
			wantEnabled:   true,
			wantImmutable: true,
		},
		{
			// A credential that may not read the bucket configuration, or a
			// backend nobody has met. "I do not know" is the only honest answer;
			// reporting unsupported would be a guess in the reassuring direction.
			name:     "access denied",
			bucket:   fakeBucket{lockErr: apiError("AccessDenied")},
			wantLock: ObjectLockUnknown,
		},
		{
			name:     "transport failure",
			bucket:   fakeBucket{lockErr: errors.New("dial tcp: connection refused")},
			wantLock: ObjectLockUnknown,
		},
		{
			name: "versioning enabled",
			bucket: fakeBucket{
				lockErr: apiError("NotImplemented"),
				verOut:  &s3.GetBucketVersioningOutput{Status: types.BucketVersioningStatusEnabled},
			},
			wantLock:      ObjectLockUnsupported,
			wantVersioned: true,
		},
		{
			// Garage answers this call successfully with an empty status,
			// exactly as a never-versioned real S3 bucket does. Not versioned is
			// all we may conclude; not supported is not.
			name: "versioning answered with an empty status",
			bucket: fakeBucket{
				lockErr: apiError("NotImplemented"),
				verOut:  &s3.GetBucketVersioningOutput{},
			},
			wantLock: ObjectLockUnsupported,
		},
		{
			// Nothing at all, neither answer nor error. No real client does
			// this, but the probe runs inside a prune and must not be able to
			// take a maintenance run down with a nil dereference.
			name:     "versioning answered with nothing",
			bucket:   fakeBucket{lockErr: apiError("NotImplemented")},
			wantLock: ObjectLockUnsupported,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := probeWith(context.Background(), tc.bucket, "bucket")
			if got.ObjectLock != tc.wantLock {
				t.Errorf("ObjectLock = %v, want %v", got.ObjectLock, tc.wantLock)
			}
			if got.ObjectLockEnabled != tc.wantEnabled {
				t.Errorf("ObjectLockEnabled = %v, want %v", got.ObjectLockEnabled, tc.wantEnabled)
			}
			if got.VersioningEnabled != tc.wantVersioned {
				t.Errorf("VersioningEnabled = %v, want %v", got.VersioningEnabled, tc.wantVersioned)
			}
			if got.Immutable() != tc.wantImmutable {
				t.Errorf("Immutable() = %v, want %v", got.Immutable(), tc.wantImmutable)
			}
		})
	}
}

// Versioning alone does not make a backup undeletable: a credential that may
// delete objects may delete versions too. Only an enabled Object Lock counts.
func TestVersioningAloneIsNotImmutability(t *testing.T) {
	caps := probeWith(context.Background(), fakeBucket{
		lockErr: apiError("NotImplemented"),
		verOut:  &s3.GetBucketVersioningOutput{Status: types.BucketVersioningStatusEnabled},
	}, "bucket")

	if !caps.VersioningEnabled {
		t.Fatal("versioning should have been observed as enabled")
	}
	if caps.Immutable() {
		t.Fatal("versioning without object lock must not count as immutable")
	}
}

func TestObjectLockSupportString(t *testing.T) {
	for support, want := range map[ObjectLockSupport]string{
		ObjectLockUnknown:     "unknown",
		ObjectLockUnsupported: "unsupported",
		ObjectLockSupported:   "supported",
	} {
		if got := support.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}
