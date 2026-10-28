package cs3

import (
	"context"
	"errors"
	"fmt"
	"testing"

	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUnreachable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		// What the fixture returned while OpenCloud booted.
		{"connection refused", wrapped(status.Error(codes.Unavailable, "connection refused")), true},
		{"call cut off", wrapped(status.Error(codes.DeadlineExceeded, "context deadline exceeded")), true},
		{"attempt timed out", wrapped(context.DeadlineExceeded), true},
		{"gateway up, service behind it not", wrapped(&StatusError{Op: "ListStorageSpaces", Code: rpc.Code_CODE_UNAVAILABLE}), true},
		// What it returned with a wrong secret: an answer, not waiting matter.
		{"wrong secret", wrapped(&StatusError{Op: "Authenticate", Code: rpc.Code_CODE_INTERNAL}), false},
		{"refused", wrapped(status.Error(codes.PermissionDenied, "no")), false},
		{"cancelled", wrapped(context.Canceled), false},
		{"anything else", errors.New("cs3state: the configured state space does not exist"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Unreachable(tc.err); got != tc.want {
				t.Fatalf("Unreachable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// The typed error reads as the formatted one it replaced, which log lines and
// the not-found check in cs3state match on.
func TestStatusErrorMessage(t *testing.T) {
	err := statusErr(&rpc.Status{Code: rpc.Code_CODE_NOT_FOUND}, "Stat")
	if got, want := err.Error(), "cs3 Stat: code=CODE_NOT_FOUND"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if statusErr(&rpc.Status{Code: rpc.Code_CODE_OK}, "Stat") != nil {
		t.Fatal("OK must not be an error")
	}
}

func wrapped(err error) error { return fmt.Errorf("cs3state: resolve state space: %w", err) }
