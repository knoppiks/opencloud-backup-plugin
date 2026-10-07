package objstore

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Without both halves of a static credential the SDK would walk its default
// chain, ending at the cloud metadata service (review-2026-10.md F5). NewS3
// refuses before anything reaches the network.
func TestNewS3RefusesMissingCredentialsWithoutANetworkCall(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(srv.Close)

	for name, creds := range map[string][2]string{
		"both empty":    {"", ""},
		"no secret":     {"id", ""},
		"no key id":     {"", "secret"},
		"whitespace id": {"  ", "secret"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewS3(t.Context(), S3Config{
				Endpoint:        srv.URL,
				Region:          "garage",
				Bucket:          "b",
				AccessKeyID:     creds[0],
				SecretAccessKey: creds[1],
			})
			if !errors.Is(err, ErrMissingCredentials) {
				t.Fatalf("NewS3 = %v, want ErrMissingCredentials", err)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("%d request(s) reached the network", n)
	}
}

func TestNewS3AcceptsStaticCredentials(t *testing.T) {
	if _, err := NewS3(t.Context(), S3Config{
		Endpoint:        "127.0.0.1:1",
		Region:          "garage",
		Bucket:          "b",
		AccessKeyID:     "id",
		SecretAccessKey: "secret",
	}); err != nil {
		t.Fatalf("NewS3: %v", err)
	}
}
