// Command decrypt is the user-side standalone decrypt CLI (Path A,
// decisions.md).
//
// It runs entirely offline: a Take-Out directory plus the user's Recovery Key,
// no network, no OpenCloud, no server. The plaintext Recovery Key never crosses
// the network and is never accepted as a command-line argument — command lines
// end up in shell history and in every process listing — so it is prompted for,
// or read from standard input when a script pipes it in.
//
// This binary is the family's last resort. Keep it small, keep it dependency-
// light, and keep it able to read *every* historical key-envelope and Take-Out
// version, forever.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/takeout"
	takeoutdecrypt "opencloud-backup-plugin/pkg/takeout/decrypt"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "decrypt: %v\n", err)
		os.Exit(1)
	}
}

type config struct {
	in         string
	out        string
	snapshotID string
	list       bool
	verify     bool
	workDir    string
}

func run(args []string) error {
	fs := flag.NewFlagSet("decrypt", flag.ContinueOnError)
	fs.Usage = func() {
		_, _ = fmt.Fprint(os.Stderr, usage)
		fs.PrintDefaults()
	}

	var cfg config
	fs.StringVar(&cfg.in, "in", "", "take-out directory (from the administrator)")
	fs.StringVar(&cfg.out, "out", "", "directory to restore your files into")
	fs.StringVar(&cfg.snapshotID, "snapshot", "", "snapshot to restore (default: the newest)")
	fs.BoolVar(&cfg.list, "list", false, "list the available snapshots and exit")
	fs.BoolVar(&cfg.verify, "verify", false, "check the take-out against its manifest and exit")
	fs.StringVar(&cfg.workDir, "work-dir", "", "directory for temporary files (default: system temp)")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.in == "" {
		return errors.New("-in is required (the take-out directory)")
	}
	if !cfg.list && !cfg.verify && cfg.out == "" {
		return errors.New("-out is required (where to restore your files)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Verification needs no key at all, so it never prompts.
	if cfg.verify {
		return verify(ctx, cfg.in)
	}

	rk, err := readRecoveryKey(os.Stdin, os.Stderr)
	if err != nil {
		return err
	}
	defer keys.Zeroize(rk)

	if cfg.list {
		return list(ctx, cfg, rk)
	}
	return decrypt(ctx, cfg, rk)
}

func verify(ctx context.Context, dir string) error {
	if err := takeout.Verify(ctx, dir); err != nil {
		return explain(err)
	}
	fmt.Println("the take-out is complete and matches its manifest")
	return nil
}

func list(ctx context.Context, cfg config, rk []byte) error {
	snaps, err := takeoutdecrypt.ListSnapshots(ctx, cfg.in, rk, cfg.workDir)
	if err != nil {
		return explain(err)
	}
	fmt.Printf("%-40s %-22s %10s %14s\n", "SNAPSHOT", "TAKEN", "FILES", "SIZE")
	for _, s := range snaps {
		fmt.Printf("%-40s %-22s %10d %14s\n",
			s.ID, s.StartTime.Local().Format(time.RFC3339), s.FileCount, humanBytes(s.TotalBytes))
	}
	return nil
}

func decrypt(ctx context.Context, cfg config, rk []byte) error {
	res, err := takeoutdecrypt.Decrypt(ctx, takeoutdecrypt.Options{
		Dir:         cfg.in,
		RecoveryKey: rk,
		OutDir:      cfg.out,
		SnapshotID:  cfg.snapshotID,
		WorkDir:     cfg.workDir,
	})
	if err != nil {
		return explain(err)
	}

	fmt.Printf("restored snapshot %s (taken %s) to %s\n",
		res.SnapshotID, res.StartTime.Local().Format(time.RFC3339), res.OutDir)
	return nil
}

// readRecoveryKey prompts for the Recovery Key without echoing it. When stdin is
// not a terminal (a script, a test) it reads one line instead, so the key can be
// piped in without ever appearing in a command line.
func readRecoveryKey(in *os.File, out io.Writer) ([]byte, error) {
	var raw []byte

	if term.IsTerminal(int(in.Fd())) {
		_, _ = fmt.Fprint(out, "Recovery Key (input hidden): ")
		typed, err := term.ReadPassword(int(in.Fd()))
		_, _ = fmt.Fprintln(out)
		if err != nil {
			return nil, fmt.Errorf("could not read the recovery key: %w", err)
		}
		raw = typed
	} else {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
			return nil, fmt.Errorf("could not read the recovery key: %w", err)
		}
		raw = []byte(line)
	}

	defer keys.Zeroize(raw)

	secret, err := keys.DecodeRecoveryKey(strings.TrimSpace(string(raw)))
	if err != nil {
		// The message describes the *shape* of the problem (checksum, charset,
		// version) and never the input itself.
		return nil, fmt.Errorf("%w\n"+
			"       recovery keys look like ocbk1-XXXXX-XXXXX-...; check for typos", err)
	}
	return secret, nil
}

// explain turns a library error into plain-language advice. The audience is a
// family member on the worst day of their digital life, not an operator.
func explain(err error) error {
	switch {
	case errors.Is(err, takeoutdecrypt.ErrWrongRecoveryKey):
		return errors.New("key does not match: this recovery key cannot open this take-out.\n" +
			"       check that you used the key for this space, and that it was copied in full")
	case errors.Is(err, takeout.ErrNoTakeOut):
		return errors.New("that folder is not a take-out (no manifest.json inside).\n" +
			"       point -in at the folder the administrator gave you")
	case errors.Is(err, takeout.ErrNoEnvelope):
		return errors.New("this take-out has no key envelope, so it cannot be decrypted.\n" +
			"       ask the administrator to extract it again after a backup has run")
	case errors.Is(err, takeoutdecrypt.ErrUnsupportedEnvelope):
		return errors.New("this take-out was written by a newer version of the backup service.\n" +
			"       use a newer 'decrypt' build to open it")
	case errors.Is(err, takeout.ErrCorrupt):
		return fmt.Errorf("this take-out is damaged: %w\n"+
			"       ask the administrator for a fresh copy", err)
	case errors.Is(err, snapshot.ErrSnapshotNotFound):
		return errors.New("no snapshot with that id in this take-out; run with -list to see them")
	default:
		return err
	}
}

// humanBytes renders a byte count for the snapshot listing.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

const usage = `decrypt — restore your files from a take-out, offline.

You need two things: the take-out folder from your administrator, and your own
Recovery Key. Nothing is sent anywhere: this program uses no network at all.

Usage:
  decrypt -in <take-out folder> -out <folder for your files>
  decrypt -in <take-out folder> -list
  decrypt -in <take-out folder> -verify

You will be asked for your Recovery Key; it is never shown as you type and is
never stored.

Flags:
`
