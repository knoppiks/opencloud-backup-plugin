//go:build integration

package testutil_test

// What Garage's grants actually permit, and what its bucket-configuration calls
// actually answer.
//
// Phase 7's Tier 2 was planned around two claims about this backend: that a
// "write-only" key would be unable to read existing backups, and that an
// "owner" key would be the one able to prune. Both are false, and the second is
// backwards. These tests are here so the design is built on the behaviour rather
// than on the plan, and so a future Garage that changes any of it fails loudly
// instead of quietly invalidating a paragraph of decisions.md.

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"opencloud-backup-plugin/internal/testutil"
)

// TestGarageGrantMatrix pins every grant combination against every operation a
// backup or a prune performs.
//
// The two rows that decide Phase 7:
//
//   - "write" allows DeleteObject. There is no grant that writes without
//     deleting, so a leaked backup credential can destroy the history it wrote.
//   - "owner" allows no object access whatsoever — it is bucket administration.
//     A prune key granted only "owner" could not delete a single snapshot.
//
// Together with "write" denying GetObject — which is what stops kopia opening a
// repository at all — both roles are forced onto read+write, i.e. onto the same
// capability. That is the finding the credential split is documented against.
func TestGarageGrantMatrix(t *testing.T) {
	ctx := context.Background()
	g := testutil.StartGarage(ctx, t)
	owner := g.S3Client(ctx, t)

	cases := []struct {
		name   string
		grants []testutil.Grant

		put, get, list, del, admin bool
	}{
		{
			name: "no grants",
		},
		{
			name:   "read",
			grants: []testutil.Grant{testutil.GrantRead},
			get:    true, list: true,
		},
		{
			// The plan's "backup-writer": write, no read. It can delete, and it
			// cannot read — so it cannot open a kopia repository either.
			name:   "write",
			grants: []testutil.Grant{testutil.GrantWrite},
			put:    true, del: true,
		},
		{
			// What both roles actually need on this backend.
			name:   "read and write",
			grants: []testutil.Grant{testutil.GrantRead, testutil.GrantWrite},
			put:    true, get: true, list: true, del: true,
		},
		{
			// The plan's "prune-owner". It cannot touch an object.
			name:   "owner",
			grants: []testutil.Grant{testutil.GrantOwner},
			admin:  true,
		},
		{
			name:   "read, write and owner",
			grants: []testutil.Grant{testutil.GrantRead, testutil.GrantWrite, testutil.GrantOwner},
			put:    true, get: true, list: true, del: true, admin: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := g.CreateKey(ctx, t, "key-"+t.Name(), tc.grants...)
			client := g.S3ClientFor(ctx, t, key)

			// Seed with the owner credential so GET and DELETE have a subject
			// that exists; otherwise "denied" and "absent" are confusable.
			probeKey := t.Name() + "/probe"
			if _, err := owner.PutObject(ctx, &s3.PutObjectInput{
				Bucket: aws.String(g.Bucket),
				Key:    aws.String(probeKey),
				Body:   bytes.NewReader([]byte("probe")),
			}); err != nil {
				t.Fatalf("seed probe object: %v", err)
			}

			_, err := client.PutObject(ctx, &s3.PutObjectInput{
				Bucket: aws.String(g.Bucket),
				Key:    aws.String(t.Name() + "/written"),
				Body:   bytes.NewReader([]byte("written")),
			})
			assertAllowed(t, "PutObject", tc.put, err)

			_, err = client.GetObject(ctx, &s3.GetObjectInput{
				Bucket: aws.String(g.Bucket), Key: aws.String(probeKey),
			})
			assertAllowed(t, "GetObject", tc.get, err)

			_, err = client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
				Bucket: aws.String(g.Bucket),
			})
			assertAllowed(t, "ListObjectsV2", tc.list, err)

			_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(g.Bucket), Key: aws.String(probeKey),
			})
			assertAllowed(t, "DeleteObject", tc.del, err)

			_, err = client.PutBucketWebsite(ctx, &s3.PutBucketWebsiteInput{
				Bucket: aws.String(g.Bucket),
				WebsiteConfiguration: &types.WebsiteConfiguration{
					IndexDocument: &types.IndexDocument{Suffix: aws.String("index.html")},
				},
			})
			assertAllowed(t, "PutBucketWebsite", tc.admin, err)
		})
	}
}

// TestGarageWriteImpliesDelete states the consequence on its own, because it is
// the single fact that decides what Tier 2 can promise: on this backend there is
// no credential that may add a backup but not destroy one.
func TestGarageWriteImpliesDelete(t *testing.T) {
	ctx := context.Background()
	g := testutil.StartGarage(ctx, t)

	writer := g.S3ClientFor(ctx, t, g.CreateKey(ctx, t, "writer", testutil.GrantWrite))
	key := "write-implies-delete"

	if _, err := writer.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(g.Bucket), Key: aws.String(key),
		Body: bytes.NewReader([]byte("a backup")),
	}); err != nil {
		t.Fatalf("a write grant must allow PutObject: %v", err)
	}
	if _, err := writer.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(g.Bucket), Key: aws.String(key),
	}); err != nil {
		t.Fatalf("expected write to imply delete on Garage, but DeleteObject failed: %v", err)
	}

	// If this ever stops being true, Garage has grown a finer permission model
	// and the credential split becomes an enforceable bound. That is worth
	// failing a build over.
	owner := g.S3Client(ctx, t)
	if _, err := owner.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(g.Bucket), Key: aws.String(key),
	}); err == nil {
		t.Fatal("the object survived a delete by a write-only key")
	}
}

// TestGarageCapabilitySignals pins what the capability probe can and cannot
// learn from this backend. The asymmetry is the interesting part: Object Lock
// answers "NotImplemented" and is therefore detectable, while versioning answers
// successfully with an empty status — byte for byte what a real S3 bucket that
// has never had versioning enabled returns.
func TestGarageCapabilitySignals(t *testing.T) {
	ctx := context.Background()
	g := testutil.StartGarage(ctx, t)
	client := g.S3Client(ctx, t)

	_, err := client.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{
		Bucket: aws.String(g.Bucket),
	})
	if err == nil {
		t.Fatal("expected Object Lock configuration to be unsupported")
	}
	assertNotImplemented(t, err, "GetObjectLockConfiguration")

	versioning, err := client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{
		Bucket: aws.String(g.Bucket),
	})
	if err != nil {
		t.Fatalf("GetBucketVersioning is expected to succeed on Garage: %v", err)
	}
	if versioning.Status != "" {
		t.Fatalf("versioning status = %q, expected the empty status that makes "+
			"'unsupported' and 'never enabled' indistinguishable", versioning.Status)
	}
}

// assertAllowed checks one operation against the expected permission.
func assertAllowed(t *testing.T, op string, want bool, err error) {
	t.Helper()
	switch {
	case want && err != nil:
		t.Errorf("%s: expected to be allowed, got %v", op, err)
	case !want && err == nil:
		t.Errorf("%s: expected to be denied, but it succeeded", op)
	case !want:
		var apiErr smithy.APIError
		if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "AccessDenied" {
			t.Errorf("%s: expected AccessDenied, got %v", op, err)
		}
	}
}
