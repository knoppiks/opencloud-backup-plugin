package snapshot

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
)

// kopia's S3 driver falls back to AWS environment variables and the cloud
// metadata service on empty credentials (review-2026-10.md F5). The opener
// refuses first, and nothing is dialled.
func TestS3OpenerRefusesMissingCredentialsWithoutANetworkCall(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var dials atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			dials.Add(1)
			_ = conn.Close()
		}
	}()

	for name, loc := range map[string]Location{
		"both empty": {},
		"no secret":  {AccessKeyID: "id"},
		"no key id":  {SecretAccessKey: "secret"},
	} {
		t.Run(name, func(t *testing.T) {
			loc.Endpoint = ln.Addr().String()
			loc.Bucket = "b"
			loc.Region = "garage"
			loc.DisableTLS = true
			_, err := S3Opener{}.Open(t.Context(), Repo{Location: loc, Space: SpaceRef{SpaceID: "s"}}, false)
			if !errors.Is(err, ErrMissingCredentials) {
				t.Fatalf("Open = %v, want ErrMissingCredentials", err)
			}
		})
	}
	if n := dials.Load(); n != 0 {
		t.Fatalf("%d connection(s) reached the endpoint", n)
	}
}
