// Command takeout is the admin Take-Out extractor CLI (Path A, decisions.md).
// It extracts ciphertext-only backup blobs from S3 for a Space; it must work
// with OpenCloud fully down and can never decrypt or take key input (the admin
// never sees plaintext — decisions.md #2, restore-paths contract).
//
// Implemented in Phase 5. Phase 1 provides only the thin entrypoint so the
// binary builds and CI stays honest.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "takeout: not implemented until Phase 5")
	os.Exit(2)
}
