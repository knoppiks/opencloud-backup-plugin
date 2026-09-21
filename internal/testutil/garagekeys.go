package testutil

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go/exec"
)

// Grant is one of Garage's bucket permissions.
//
// Garage has exactly these three and nothing finer (`garage bucket allow
// --help` on the pinned image). Two consequences drive the credential split in
// phase 7, and both are asserted by TestGarageGrantMatrix rather than taken
// from Garage's documentation:
//
//   - GrantOwner is about administering the *bucket* — deleting it, changing its
//     website configuration. It is not what lets a key delete an object; that is
//     GrantWrite. So "the maintenance key is the owner key" buys nothing.
//   - There is no grant that permits writing without also permitting deleting,
//     and none that permits writing without reading. So a key that can add a
//     backup can also destroy every backup it can see.
type Grant string

const (
	GrantRead  Grant = "--read"
	GrantWrite Grant = "--write"
	GrantOwner Grant = "--owner"
)

// Key is one Garage access key pair.
type Key struct {
	AccessKeyID     string
	SecretAccessKey string
}

// CreateKey creates a named Garage access key and grants it exactly the given
// permissions on the fixture's bucket. Passing no grants yields a key with
// access to nothing, which is a useful control.
func (g *Garage) CreateKey(ctx context.Context, t *testing.T, name string, grants ...Grant) Key {
	t.Helper()

	out := g.runGarage(ctx, t, "key", "create", name)
	key := Key{
		AccessKeyID:     scanField(t, out, "Key ID:"),
		SecretAccessKey: scanField(t, out, "Secret key:"),
	}

	if len(grants) > 0 {
		args := []string{"bucket", "allow"}
		for _, grant := range grants {
			args = append(args, string(grant))
		}
		args = append(args, g.Bucket, "--key", name)
		g.runGarage(ctx, t, args...)
	}

	return key
}

// runGarage executes the Garage CLI inside the container, against the node it
// is running. Output is returned combined: Garage logs its RPC handshake to
// stderr on every invocation, and the fields we parse are labelled uniquely
// enough that separating the streams would only add a dependency on which one
// Garage chose.
func (g *Garage) runGarage(ctx context.Context, t *testing.T, args ...string) string {
	t.Helper()

	code, reader, err := g.container.Exec(ctx, append([]string{"/garage"}, args...),
		exec.Multiplexed())
	if err != nil {
		t.Fatalf("garage %s: %v", strings.Join(args, " "), err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("garage %s: read output: %v", strings.Join(args, " "), err)
	}
	if code != 0 {
		t.Fatalf("garage %s: exit %d\n%s", strings.Join(args, " "), code, body)
	}
	return string(body)
}

// scanField pulls the value of a "Label: value" line out of Garage CLI output.
func scanField(t *testing.T, out, label string) string {
	t.Helper()

	for line := range strings.SplitSeq(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(trimmed, label); ok {
			value := strings.TrimSpace(after)
			if value == "" {
				t.Fatalf("garage output: %q is empty", label)
			}
			return value
		}
	}
	t.Fatalf("garage output: no %q line", label)
	return ""
}
