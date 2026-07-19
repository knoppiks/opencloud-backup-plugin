// Command decrypt is the user-side standalone decrypt CLI (Path A, decisions.md).
// It runs entirely offline, independent of OpenCloud and the server: the user
// supplies their Recovery Key locally to decrypt a Take-Out blob. The plaintext
// RK never crosses the network (decisions.md, trust/key model).
//
// It must parse every historical key-envelope version forever (envelope format
// is a long-term compatibility promise). Implemented in Phase 5; Phase 1
// provides only the thin entrypoint.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "decrypt: not implemented until Phase 5")
	os.Exit(2)
}
