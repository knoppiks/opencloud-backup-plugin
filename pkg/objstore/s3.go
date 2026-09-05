package objstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Config describes how to reach an S3 target. Credentials are plaintext and
// therefore live only in process memory for the duration of a command or run;
// they are never logged and never serialized (decisions.md #14).
type S3Config struct {
	// Endpoint is a URL or a bare host:port.
	Endpoint string
	Region   string
	Bucket   string
	// AccessKeyID / SecretAccessKey are the target's S3 credentials. When both
	// are empty the ambient AWS configuration (env, shared config) is used.
	AccessKeyID     string
	SecretAccessKey string
	// UsePathStyle forces path-style addressing (required for Garage).
	UsePathStyle bool
	// DisableTLS talks plain HTTP; it also decides the scheme when Endpoint
	// carries none.
	DisableTLS bool
}

// S3Store is the S3-backed Store.
type S3Store struct {
	client *s3.Client
	bucket string
}

var _ Store = (*S3Store)(nil)

// NewS3 builds an S3-backed store.
//
// The SDK's default checksum behaviour (WhenSupported) breaks multipart GET
// against Garage, which returns no SDK-verifiable composite checksum
// (phase-0-findings.md Spike 1, gotcha 4), so both checksum knobs are pinned to
// WhenRequired.
func NewS3(ctx context.Context, cfg S3Config) (*S3Store, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("objstore: bucket is required")
	}

	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
		awsconfig.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
	}
	if cfg.AccessKeyID != "" || cfg.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		// The error may quote configuration file paths but never credentials.
		return nil, fmt.Errorf("objstore: load s3 configuration: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(EndpointURL(cfg.Endpoint, cfg.DisableTLS))
		}
		o.UsePathStyle = cfg.UsePathStyle
	})
	return &S3Store{client: client, bucket: cfg.Bucket}, nil
}

// EndpointURL normalises a bare host:port into a URL, leaving an explicit
// scheme untouched.
func EndpointURL(endpoint string, disableTLS bool) string {
	endpoint = strings.TrimSpace(endpoint)
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return strings.TrimSuffix(endpoint, "/")
	}
	scheme := "https://"
	if disableTLS {
		scheme = "http://"
	}
	return scheme + strings.TrimSuffix(endpoint, "/")
}

// Get streams one object.
func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNoSuchKey(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, fmt.Errorf("objstore: get %s: %w", key, err)
	}
	return out.Body, nil
}

// Put stores one object.
func (s *S3Store) Put(ctx context.Context, key string, body []byte) error {
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
	}); err != nil {
		return fmt.Errorf("objstore: put %s: %w", key, err)
	}
	return nil
}

// List enumerates every object under a prefix, following pagination.
func (s *S3Store) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	pager := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("objstore: list %s: %w", prefix, err)
		}
		for _, o := range page.Contents {
			out = append(out, ObjectInfo{
				Key:  aws.ToString(o.Key),
				Size: aws.ToInt64(o.Size),
			})
		}
	}
	return out, nil
}

// isNoSuchKey recognises a missing object across the shapes S3-compatible
// servers return it in.
func isNoSuchKey(err error) bool {
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var nf *types.NotFound
	return errors.As(err, &nf)
}
