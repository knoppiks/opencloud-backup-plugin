//go:build integration

package testutil_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"opencloud-backup-plugin/internal/testutil"
)

// TestGarageRoundTrip is Spike 1's exit criterion: one helper brings up a clean
// Garage and a simple put/get/list round-trip passes.
func TestGarageRoundTrip(t *testing.T) {
	ctx := context.Background()
	g := testutil.StartGarage(ctx, t)
	client := g.S3Client(ctx, t)

	key := "hello.txt"
	want := []byte("hello garage fixture")

	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(g.Bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(want),
	}); err != nil {
		t.Fatalf("put object: %v", err)
	}

	list, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(g.Bucket),
	})
	if err != nil {
		t.Fatalf("list objects: %v", err)
	}
	if len(list.Contents) != 1 || aws.ToString(list.Contents[0].Key) != key {
		t.Fatalf("list mismatch: %+v", list.Contents)
	}

	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(g.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	got, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, want)
	}
}

// TestGarageMultipart verifies a >5MiB object uploaded via the S3 transfer
// manager (which uses multipart) round-trips byte-identically.
func TestGarageMultipart(t *testing.T) {
	ctx := context.Background()
	g := testutil.StartGarage(ctx, t)
	client := g.S3Client(ctx, t)

	// 24 MiB random payload with a 5 MiB part size => multiple parts.
	payload := make([]byte, 24<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("gen payload: %v", err)
	}
	wantSum := sha256.Sum256(payload)

	uploader := manager.NewUploader(client, func(u *manager.Uploader) {
		u.PartSize = 5 << 20
		u.Concurrency = 3
	})
	key := "big.bin"
	if _, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(g.Bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(payload),
	}); err != nil {
		t.Fatalf("multipart upload: %v", err)
	}

	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(g.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	defer out.Body.Close()
	got, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if gotSum := sha256.Sum256(got); gotSum != wantSum {
		t.Fatalf("multipart checksum mismatch: got %x want %x", gotSum, wantSum)
	}
}

// TestGarageNoObjectLock records that Garage rejects Object-Lock configuration
// (decisions.md #8). If a future Garage gains Object Lock this test will fail,
// signalling that the capability probe / immutability tiers can be revisited.
func TestGarageNoObjectLock(t *testing.T) {
	ctx := context.Background()
	g := testutil.StartGarage(ctx, t)
	client := g.S3Client(ctx, t)

	_, err := client.PutObjectLockConfiguration(ctx, &s3.PutObjectLockConfigurationInput{
		Bucket: aws.String(g.Bucket),
		ObjectLockConfiguration: &types.ObjectLockConfiguration{
			ObjectLockEnabled: types.ObjectLockEnabledEnabled,
		},
	})
	if err == nil {
		t.Fatal("expected Object Lock to be unsupported, but PutObjectLockConfiguration succeeded")
	}
	assertNotImplemented(t, err, "PutObjectLockConfiguration")
}

// TestGarageNoVersioning records that Garage rejects enabling bucket versioning
// (decisions.md #8).
func TestGarageNoVersioning(t *testing.T) {
	ctx := context.Background()
	g := testutil.StartGarage(ctx, t)
	client := g.S3Client(ctx, t)

	_, err := client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(g.Bucket),
		VersioningConfiguration: &types.VersioningConfiguration{
			Status: types.BucketVersioningStatusEnabled,
		},
	})
	if err == nil {
		t.Fatal("expected versioning to be unsupported, but PutBucketVersioning succeeded")
	}
	assertNotImplemented(t, err, "PutBucketVersioning")
}

// assertNotImplemented fails unless err is an S3 API error with code
// "NotImplemented".
func assertNotImplemented(t *testing.T, err error, op string) {
	t.Helper()
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("%s: expected smithy.APIError, got %T: %v", op, err, err)
	}
	if apiErr.ErrorCode() != "NotImplemented" {
		t.Fatalf("%s: expected NotImplemented, got %q: %v", op, apiErr.ErrorCode(), err)
	}
}
