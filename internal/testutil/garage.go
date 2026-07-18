// Package testutil provides reusable integration-test fixtures.
//
// The Garage fixture (this file) brings up an ephemeral, single-node Garage S3
// server in a container. It is the S3 target used by integration tests across
// all phases. Garage is pinned to a known-good tag (see GarageImage); as of
// that version Garage has no S3 Object Lock and no bucket versioning (see
// decisions.md #8), so tests must not depend on those features.
package testutil

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// GarageImage is the pinned Garage image. Single-node auto-config
// (--single-node / --default-bucket) requires >= v2.3.0 (see phase-0 spikes).
const GarageImage = "dxflrs/garage:v2.3.0"

const (
	garageS3Port    = "3900/tcp"
	garageAdminPort = "3903/tcp"

	// Default credentials/bucket seeded into the ephemeral instance. These are
	// test-only values; the secret key must be >= 40 chars for Garage.
	defaultAccessKey = "GK00000000000000000000ff"
	defaultSecretKey = "0000000000000000000000000000000000000000000000000000000000000000"
	defaultBucket    = "testbucket"

	// Region Garage advertises. S3 clients must use this and path-style addressing.
	garageRegion = "garage"
)

// minimal single-node config. Ports match the exposed container ports; the
// RPC secret is a throwaway all-zero key (single node never talks to peers).
const garageConfig = `
metadata_dir = "/tmp/meta"
data_dir = "/tmp/data"
db_engine = "sqlite"
replication_factor = 1

rpc_bind_addr = "[::]:3901"
rpc_secret = "0000000000000000000000000000000000000000000000000000000000000000"

[s3_api]
s3_region = "garage"
api_bind_addr = "[::]:3900"
root_domain = ".s3.garage.localhost"

[admin]
api_bind_addr = "[::]:3903"
admin_token = "test-admin-token"
`

// Garage is a running ephemeral Garage instance and the connection details a
// path-style SigV4 S3 client needs to talk to it.
type Garage struct {
	container testcontainers.Container

	// Endpoint is the S3 API base URL, e.g. http://127.0.0.1:49xxx.
	Endpoint string
	// Region is the region Garage advertises ("garage").
	Region string
	// AccessKeyID / SecretAccessKey are the seeded default S3 credentials.
	AccessKeyID     string
	SecretAccessKey string
	// Bucket is the pre-created default bucket.
	Bucket string
}

// StartGarage brings up a clean, ephemeral Garage container with a default
// bucket and access key already provisioned. It registers cleanup via
// t.Cleanup, so callers do not terminate it themselves.
//
// Data lives in the container's /tmp (no volumes), so every StartGarage yields
// a pristine store. Intended for use behind the `integration` build tag.
func StartGarage(ctx context.Context, t *testing.T) *Garage {
	t.Helper()

	req := testcontainers.ContainerRequest{
		Image:        GarageImage,
		ExposedPorts: []string{garageS3Port, garageAdminPort},
		Entrypoint:   []string{"/garage"},
		Cmd:          []string{"server", "--single-node", "--default-bucket"},
		Env: map[string]string{
			"GARAGE_DEFAULT_ACCESS_KEY": defaultAccessKey,
			"GARAGE_DEFAULT_SECRET_KEY": defaultSecretKey,
			"GARAGE_DEFAULT_BUCKET":     defaultBucket,
		},
		Files: []testcontainers.ContainerFile{
			{
				Reader:            strings.NewReader(garageConfig),
				ContainerFilePath: "/etc/garage.toml",
				FileMode:          0o644,
			},
		},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort(garageS3Port),
			wait.ForLog("Creating default bucket").WithStartupTimeout(60*time.Second),
		),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start garage container: %v", err)
	}
	t.Cleanup(func() {
		// Best-effort teardown; use a fresh context so it runs even if the
		// test's context is already cancelled.
		_ = container.Terminate(context.Background())
	})

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("garage host: %v", err)
	}
	mapped, err := container.MappedPort(ctx, garageS3Port)
	if err != nil {
		t.Fatalf("garage mapped port: %v", err)
	}

	return &Garage{
		container:       container,
		Endpoint:        fmt.Sprintf("http://%s:%s", host, mapped.Port()),
		Region:          garageRegion,
		AccessKeyID:     defaultAccessKey,
		SecretAccessKey: defaultSecretKey,
		Bucket:          defaultBucket,
	}
}
