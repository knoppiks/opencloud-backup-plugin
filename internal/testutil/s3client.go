package testutil

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Client returns an AWS SDK v2 S3 client configured for this Garage instance:
// static credentials, the Garage region, the container endpoint, and path-style
// addressing (Garage does not support virtual-host style by default).
func (g *Garage) S3Client(ctx context.Context, t *testing.T) *s3.Client {
	t.Helper()

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(g.Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(g.AccessKeyID, g.SecretAccessKey, ""),
		),
		// Garage does not implement the newer default S3 checksum behaviour the
		// AWS SDK v2 enables by default (WhenSupported). In particular it does
		// not return an SDK-verifiable composite checksum for multipart objects,
		// which makes GetObject fail checksum validation. Fall back to
		// when-required, which matches Garage's S3 semantics.
		awsconfig.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
		awsconfig.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
	)
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}

	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(g.Endpoint)
		o.UsePathStyle = true
	})
}
