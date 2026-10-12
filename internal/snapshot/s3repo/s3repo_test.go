package s3repo

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"

	"github.com/knoppiks/opencloud-backup-plugin/internal/snapshot"
)

func repoAt(loc snapshot.Location, spaceID string) snapshot.Repo {
	return snapshot.Repo{Location: loc, Space: snapshot.SpaceRef{SpaceID: spaceID}}
}

func TestOpenerValidation(t *testing.T) {
	creds := snapshot.Location{Bucket: "b", AccessKeyID: "id", SecretAccessKey: "secret"}

	noBucket := creds
	noBucket.Bucket = ""
	if _, err := (Opener{}).Open(t.Context(), repoAt(noBucket, "space-1"), false); err == nil {
		t.Fatal("missing bucket must be rejected")
	}
	if _, err := (Opener{}).Open(t.Context(), repoAt(creds, ""), false); err == nil {
		t.Fatal("missing space id must be rejected")
	}
}

// kopia's S3 driver falls back to AWS environment variables and the cloud
// metadata service on empty credentials (review-2026-10.md F5). The opener
// refuses first, and nothing is dialled.
func TestOpenerRefusesMissingCredentialsWithoutANetworkCall(t *testing.T) {
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

	for name, loc := range map[string]snapshot.Location{
		"both empty": {},
		"no secret":  {AccessKeyID: "id"},
		"no key id":  {SecretAccessKey: "secret"},
	} {
		t.Run(name, func(t *testing.T) {
			loc.Endpoint = ln.Addr().String()
			loc.Bucket = "b"
			loc.Region = "garage"
			loc.DisableTLS = true
			_, err := Opener{}.Open(t.Context(), repoAt(loc, "s"), false)
			if !errors.Is(err, ErrMissingCredentials) {
				t.Fatalf("Open = %v, want ErrMissingCredentials", err)
			}
		})
	}
	if n := dials.Load(); n != 0 {
		t.Fatalf("%d connection(s) reached the endpoint", n)
	}
}

func TestStripScheme(t *testing.T) {
	for in, want := range map[string]string{
		"http://garage:3900":   "garage:3900",
		"https://garage:3900":  "garage:3900",
		"garage:3900":          "garage:3900",
		"https://garage:3900/": "garage:3900",
	} {
		if got := stripScheme(in); got != want {
			t.Fatalf("stripScheme(%q) = %q, want %q", in, got, want)
		}
	}
}
