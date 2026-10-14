// Command install-bundle is the web image's entrypoint: it copies the Backup
// Vault bundle from the image into the extensions volume of an initContainer.
//
// Run without arguments it does what the documented initContainer expects,
// the same as the shell one-liner OpenCloud's own web-extensions images are
// run with:
//
//	mkdir -p /extensions/backup-vault && \
//	  cp -R /usr/share/nginx/html/backup-vault/ /extensions/
//
// The image has no shell (phase-11 §11.1, #99), so an initContainer must not
// override the command with that one-liner; it runs this instead.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/knoppiks/opencloud-backup-plugin/internal/buildinfo"
	"github.com/knoppiks/opencloud-backup-plugin/internal/bundleinstall"
	"github.com/knoppiks/opencloud-backup-plugin/internal/cli"
)

const program = "install-bundle"

// The paths the image and the initContainer agree on. The bundle sits where
// OpenCloud's web-extensions images keep theirs.
const (
	defaultFrom = "/usr/share/nginx/html/backup-vault"
	defaultTo   = "/extensions/backup-vault"
)

const usage = `Usage: install-bundle [-from DIR] [-to DIR]

Copies the Backup Vault web bundle into the extensions volume, creating the
destination if needed. Files the bundle has are replaced; others are left
alone. From there it is copied into OpenCloud's apps directory
(<OC_DATA_DIR>/web/assets/apps/backup-vault/).

`

type config struct {
	from    string
	to      string
	version bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the whole program behind main, returning its exit status
// (internal/cli).
func run(args []string, stdout, stderr io.Writer) int {
	return cli.Report(stderr, program, execute(args, stdout, stderr))
}

func flags(cfg *config) *flag.FlagSet {
	fs := flag.NewFlagSet(program, flag.ContinueOnError)
	fs.Usage = func() {
		_, _ = fmt.Fprint(fs.Output(), usage)
		fs.PrintDefaults()
	}
	fs.StringVar(&cfg.from, "from", defaultFrom, "the bundle in the image")
	fs.StringVar(&cfg.to, "to", defaultTo, "where to install it")
	fs.BoolVar(&cfg.version, "version", false, "print the version and exit")
	return fs
}

func execute(args []string, stdout, stderr io.Writer) error {
	var cfg config
	if err := cli.Parse(flags(&cfg), args, stdout, stderr); err != nil {
		return err
	}
	if cfg.version {
		_, err := fmt.Fprintln(stdout, buildinfo.Get().Line(program))
		return err
	}
	res, err := bundleinstall.Install(os.DirFS(cfg.from), cfg.to)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "installed the Backup Vault bundle into %s (%d files, %d bytes)\n",
		cfg.to, res.Files, res.Bytes)
	return err
}
