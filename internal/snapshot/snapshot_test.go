package snapshot

import "testing"

func TestSpaceRefZeroValue(t *testing.T) {
	var r SpaceRef
	if r.SpaceID != "" {
		t.Fatalf("unexpected non-zero SpaceRef: %+v", r)
	}
}
