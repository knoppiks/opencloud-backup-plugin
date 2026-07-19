package s3target

import "testing"

// TestCapabilitiesDefaultFalse documents the Garage baseline (decisions.md #8):
// no Object Lock, no versioning until proven otherwise by a probe.
func TestCapabilitiesDefaultFalse(t *testing.T) {
	var c Capabilities
	if c.ObjectLock || c.BucketVersioning {
		t.Fatalf("capabilities must default to false, got %+v", c)
	}
}
