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

	"github.com/knoppiks/opencloud-backup-plugin/internal/buildinfo"
	"github.com/knoppiks/opencloud-backup-plugin/internal/cli"
	"github.com/knoppiks/opencloud-backup-plugin/internal/snapshot"
	"github.com/knoppiks/opencloud-backup-plugin/pkg/keys"
	"github.com/knoppiks/opencloud-backup-plugin/pkg/takeout"
	takeoutdecrypt "github.com/knoppiks/opencloud-backup-plugin/pkg/takeout/decrypt"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// program is the name messages are signed with.
const program = "decrypt"

type config struct {
	in         string
	out        string
	snapshotID string
	list       bool
	verify     bool
	workDir    string
	envelope   string
	version    bool
}

// run is the whole program behind main, returning its exit status
// (internal/cli). It reads the Recovery Key from stdin, writes what was asked
// for to stdout, and everything else to stderr.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return cli.Report(stderr, program, execute(ctx, args, stdin, stdout, stderr))
}

// flags defines the CLI's entire input surface. The Recovery Key is not on
// it, and must never be.
func flags(cfg *config) *flag.FlagSet {
	fs := flag.NewFlagSet(program, flag.ContinueOnError)
	fs.Usage = func() {
		_, _ = fmt.Fprint(fs.Output(), usage)
		fs.PrintDefaults()
	}
	fs.StringVar(&cfg.in, "in", "", "take-out directory (from the administrator)")
	fs.StringVar(&cfg.out, "out", "", "directory to restore your files into")
	fs.StringVar(&cfg.snapshotID, "snapshot", "", "snapshot to restore (default: the newest)")
	fs.BoolVar(&cfg.list, "list", false, "list the available snapshots and exit")
	fs.BoolVar(&cfg.verify, "verify", false, "check the take-out against its manifest and exit")
	fs.StringVar(&cfg.workDir, "work-dir", "", "directory for temporary files (default: system temp)")
	fs.StringVar(&cfg.envelope, "envelope", "",
		"recovery.ocbke downloaded from Backup Vault, used instead of the take-out's own")
	fs.BoolVar(&cfg.version, "version", false, "print the version and exit")
	return fs
}

func execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	var cfg config
	if err := cli.Parse(flags(&cfg), args, stdout, stderr); err != nil {
		return err
	}
	if cfg.version {
		_, err := fmt.Fprintln(stdout, buildinfo.Get().Line(program))
		return err
	}
	if cfg.in == "" {
		return cli.Usagef("-in is required (the take-out directory)")
	}
	if !cfg.list && !cfg.verify && cfg.out == "" {
		return cli.Usagef("-out is required (where to restore your files)")
	}

	// Verification needs no key at all, so it never prompts.
	if cfg.verify {
		return verify(ctx, cfg.in, stdout)
	}

	rk, err := readRecoveryKey(stdin, stderr)
	if err != nil {
		return err
	}
	defer keys.Zeroize(rk)

	if cfg.list {
		return list(ctx, cfg, rk, stdout)
	}
	return decrypt(ctx, cfg, rk, stdout)
}

func verify(ctx context.Context, dir string, stdout io.Writer) error {
	if err := takeout.Verify(ctx, dir); err != nil {
		return explain(err)
	}
	_, err := fmt.Fprintln(stdout, "the take-out is complete and matches its manifest")
	return err
}

func list(ctx context.Context, cfg config, rk []byte, stdout io.Writer) error {
	snaps, err := takeoutdecrypt.ListSnapshots(ctx, cfg.options(rk))
	if err != nil {
		return explain(err)
	}
	w := &errWriter{w: stdout}
	w.printf("%-40s %-22s %10s %14s\n", "SNAPSHOT", "TAKEN", "FILES", "SIZE")
	for _, s := range snaps {
		w.printf("%-40s %-22s %10d %14s\n",
			s.ID, s.StartTime.Local().Format(time.RFC3339), s.FileCount, humanBytes(s.TotalBytes))
	}
	return w.err
}

func decrypt(ctx context.Context, cfg config, rk []byte, stdout io.Writer) error {
	res, err := takeoutdecrypt.Decrypt(ctx, cfg.options(rk))
	if err != nil {
		return explain(err)
	}

	_, err = fmt.Fprintf(stdout, "restored snapshot %s (taken %s) to %s\n",
		res.SnapshotID, res.StartTime.Local().Format(time.RFC3339), res.OutDir)
	return err
}

// errWriter keeps the first write error, so a listing is not a wall of
// error checks.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, a ...any) {
	if e.err == nil {
		_, e.err = fmt.Fprintf(e.w, format, a...)
	}
}

// options turns the flags into the library's options.
func (cfg config) options(rk []byte) takeoutdecrypt.Options {
	return takeoutdecrypt.Options{
		Dir:          cfg.in,
		RecoveryKey:  rk,
		OutDir:       cfg.out,
		SnapshotID:   cfg.snapshotID,
		WorkDir:      cfg.workDir,
		EnvelopeFile: cfg.envelope,
	}
}

// readRecoveryKey prompts for the Recovery Key without echoing it. When stdin is
// not a terminal (a script, a test) it reads one line instead, so the key can be
// piped in without ever appearing in a command line.
func readRecoveryKey(in io.Reader, out io.Writer) ([]byte, error) {
	var raw []byte

	if fd, ok := terminal(in); ok {
		_, _ = fmt.Fprint(out, "Recovery Key (input hidden): ")
		typed, err := term.ReadPassword(fd)
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

// terminal returns in's file descriptor when in is an interactive terminal.
func terminal(in io.Reader) (int, bool) {
	f, ok := in.(interface{ Fd() uintptr })
	if !ok {
		return 0, false
	}
	fd := int(f.Fd())
	return fd, term.IsTerminal(fd)
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
	case errors.Is(err, takeoutdecrypt.ErrBadEnvelopeFile):
		return errors.New("the file given with -envelope is not a recovery key envelope.\n" +
			"       download recovery.ocbke again from Backup Vault, or leave -envelope out")
	case errors.Is(err, takeoutdecrypt.ErrEnvelopeFileMismatch):
		return errors.New("the file given with -envelope does not belong to this take-out.\n" +
			"       check that you downloaded it from the same space the take-out is from")
	case errors.Is(err, takeoutdecrypt.ErrUnsupportedEnvelope):
		return errors.New("this take-out was written by a newer version of the backup service.\n" +
			"       use a newer 'decrypt' build to open it")
	case errors.Is(err, takeout.ErrNewerTakeOut):
		return errors.New("this take-out was written in a newer format than this tool reads.\n" +
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

If your Recovery Key was replaced recently, the take-out may still hold the
envelope for the old key. Download recovery.ocbke for the space from Backup
Vault ("Recovery Key" page) and pass it with -envelope.

Exit status: 0 done, 1 something went wrong (for example a wrong Recovery Key
or a damaged take-out), 2 the command line was wrong.

Flags:
`
