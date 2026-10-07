package cs3

import "testing"

// TestSpaceZeroValue is a trivial guard so CI compiles and exercises the
// package. Real behaviour is tested in Phase 2.
func TestSpaceZeroValue(t *testing.T) {
	var s Space
	if s.ID != "" || s.Members != nil {
		t.Fatalf("unexpected non-zero Space zero value: %+v", s)
	}
}
