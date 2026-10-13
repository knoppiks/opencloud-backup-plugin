package cs3

import (
	"math"
	"testing"
)

// TestSpaceZeroValue is a trivial guard so CI compiles and exercises the
// package. Real behaviour is tested in Phase 2.
func TestSpaceZeroValue(t *testing.T) {
	var s Space
	if s.ID != "" || s.Members != nil {
		t.Fatalf("unexpected non-zero Space zero value: %+v", s)
	}
}

func TestClampInt64(t *testing.T) {
	for in, want := range map[uint64]int64{
		0:                 0,
		42:                42,
		math.MaxInt64:     math.MaxInt64,
		math.MaxInt64 + 1: math.MaxInt64,
		math.MaxUint64:    math.MaxInt64,
	} {
		if got := clampInt64(in); got != want {
			t.Errorf("clampInt64(%d) = %d, want %d", in, got, want)
		}
	}
}
