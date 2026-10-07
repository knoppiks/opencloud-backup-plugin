// Command gen writes the environment reference generated from the
// configuration structs (config.Reference). Run it through go generate:
//
//	go generate ./internal/config
package main

import (
	"flag"
	"fmt"
	"os"

	"opencloud-backup-plugin/internal/config"
)

func main() {
	out := flag.String("o", "", "file to write the reference to")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "gen: -o is required")
		os.Exit(2)
	}
	if err := os.WriteFile(*out, config.Reference(), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "gen: %v\n", err)
		os.Exit(1)
	}
}
