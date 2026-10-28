package cs3

// Telling "OpenCloud is not up yet" from "OpenCloud said no".
//
// While OpenCloud boots, the gateway is not listening yet, resets the
// connection, or is not resolvable at all: every call fails at the transport,
// as gRPC Unavailable. Once it listens it answers, and an answer with a CS3
// status is OpenCloud speaking, about a request it understood. A wrong
// service-account secret is such an answer (CODE_INTERNAL from Authenticate),
// and waiting does not fix it (phase-0-findings.md, "Phase 12 spike" §4).

import (
	"context"
	"errors"
	"fmt"

	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// StatusError is a CS3 status other than OK, answered by the gateway.
type StatusError struct {
	// Op is the gateway call that answered.
	Op string
	// Code is the CS3 status it answered with.
	Code rpc.Code
}

func (e *StatusError) Error() string { return fmt.Sprintf("cs3 %s: code=%s", e.Op, e.Code) }

// Unreachable reports whether err means the gateway could not be asked, as
// opposed to having answered. It is what waiting for OpenCloud waits out.
//
// CS3 CODE_UNAVAILABLE counts as unreachable too: it was not seen on the
// fixture, but a gateway that is up before the service behind it is would
// answer with it.
func Unreachable(err error) bool {
	if err == nil {
		return false
	}
	if st := new(StatusError); errors.As(err, &st) {
		return st.Code == rpc.Code_CODE_UNAVAILABLE
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	st, ok := status.FromError(err)
	return ok && (st.Code() == codes.Unavailable || st.Code() == codes.DeadlineExceeded)
}
