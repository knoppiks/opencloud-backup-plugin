// Command takeout is the admin Take-Out extractor CLI (Path A, decisions.md).
//
// It copies a Space's ciphertext out of the S3 target into a self-contained
// Take-Out directory. It works with OpenCloud fully down — it talks to S3 and
// nothing else — and it **cannot decrypt anything**: there is no flag, prompt,
// or environment variable here that accepts a Recovery Key, a Data Key, or a
// repository password, and none may ever be added (decisions.md #2, #15).
//
// The admin hands the resulting directory to the user, who decrypts it locally
// with the `decrypt` command and their own Recovery Key.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/knoppiks/opencloud-backup-plugin/internal/buildinfo"
	"github.com/knoppiks/opencloud-backup-plugin/internal/cli"
	envconfig "github.com/knoppiks/opencloud-backup-plugin/internal/config"
	"github.com/knoppiks/opencloud-backup-plugin/internal/objstore"
	"github.com/knoppiks/opencloud-backup-plugin/internal/snapshot"
	"github.com/knoppiks/opencloud-backup-plugin/internal/snapshot/s3repo"
	"github.com/knoppiks/opencloud-backup-plugin/internal/takeout/remote"
	"github.com/knoppiks/opencloud-backup-plugin/pkg/takeout"
)

// config is the CLI's full input surface. Every field addresses the *target*;
// none of them is, or may become, key material.
type config struct {
	endpoint     string
	region       string
	bucket       string
	prefix       string
	spaceID      string
	outDir       string
	accessKey    string
	secretKey    string
	usePathStyle bool
	plainHTTP    bool

	allowMissingEnvelope bool
	verify               bool
	verbose              bool
	version              bool

	// usedInsecure records that the deprecated spelling of -plain-http was
	// given, so it can be warned about.
	usedInsecure bool
}

// program is the name messages are signed with.
const program = "takeout"

func main() {
	os.Exit(run(os.Args[1:], os.Environ(), os.Stdout, os.Stderr, s3Target))
}

// openTarget connects to the store a Take-Out is copied from: the repository
// blobs and the published key envelope. Production uses S3 (s3Target); a
// test hands in a directory.
type openTarget func(ctx context.Context, cfg config) (snapshot.StorageOpener, objstore.Store, error)

// run is the whole program behind main, returning its exit status
// (internal/cli). The summary goes to stdout; progress, warnings and errors
// to stderr.
func run(args, environ []string, stdout, stderr io.Writer, open openTarget) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return cli.Report(stderr, program, execute(ctx, args, environ, stdout, stderr, open))
}

func execute(ctx context.Context, args, environ []string, stdout, stderr io.Writer, open openTarget) error {
	var cfg config
	if err := cli.Parse(flags(&cfg), args, stdout, stderr); err != nil {
		return err
	}
	if cfg.version {
		_, err := fmt.Fprintln(stdout, buildinfo.Get().Line(program))
		return err
	}
	if cfg.usedInsecure {
		// Renamed options keep working for at least one minor release, with
		// a warning naming the replacement (compatibility-policy.md §2).
		_, _ = fmt.Fprintf(stderr,
			"%s: -insecure is deprecated and will be removed in a later release; use -plain-http\n", program)
	}
	if err := requireFlags(cfg); err != nil {
		return err
	}

	// S3 credentials come from the environment, not from flags: command lines
	// are visible to every process on the host and land in shell history.
	env, err := envconfig.LoadTakeout(environ)
	if err != nil {
		return err
	}
	cfg.accessKey = env.S3.AccessKeyID.Reveal()
	cfg.secretKey = env.S3.SecretAccessKey.Reveal()
	if err := requireCredentials(cfg); err != nil {
		return err
	}

	return extract(ctx, cfg, open, stdout, stderr)
}

// flags defines the CLI's entire input surface. It is a separate function so a
// test can audit it: no flag here may ever accept key material.
func flags(cfg *config) *flag.FlagSet {
	fs := flag.NewFlagSet(program, flag.ContinueOnError)
	fs.Usage = func() {
		_, _ = fmt.Fprint(fs.Output(), usage)
		fs.PrintDefaults()
	}

	fs.StringVar(&cfg.endpoint, "endpoint", "", "S3 endpoint (host:port or URL)")
	fs.StringVar(&cfg.region, "region", "garage", "S3 region")
	fs.StringVar(&cfg.bucket, "bucket", "", "S3 bucket holding the backups")
	fs.StringVar(&cfg.prefix, "prefix", "", "deployment prefix inside the bucket")
	fs.StringVar(&cfg.spaceID, "space", "", "id of the space to extract")
	fs.StringVar(&cfg.outDir, "out", "", "directory to write the take-out to")
	fs.BoolVar(&cfg.usePathStyle, "path-style", true, "use path-style S3 addressing (required for Garage)")
	fs.BoolVar(&cfg.plainHTTP, "plain-http", false, "talk plain HTTP to the endpoint, without TLS")
	fs.Var(deprecatedAlias{target: &cfg.plainHTTP, used: &cfg.usedInsecure}, "insecure",
		"deprecated spelling of -plain-http")
	fs.BoolVar(&cfg.allowMissingEnvelope, "allow-missing-envelope", false,
		"extract even if no recovery envelope is stored (the result cannot be decrypted on its own)")
	fs.BoolVar(&cfg.verify, "verify", true, "re-read the take-out and check it against its manifest")
	fs.BoolVar(&cfg.verbose, "v", false, "log progress")
	fs.BoolVar(&cfg.version, "version", false, "print the version and exit")
	return fs
}

// deprecatedAlias is a boolean flag kept under an old name. It sets the same
// value as its replacement and remembers that the old name was used.
type deprecatedAlias struct {
	target *bool
	used   *bool
}

func (d deprecatedAlias) String() string {
	if d.target == nil {
		return "false"
	}
	return strconv.FormatBool(*d.target)
}

func (d deprecatedAlias) Set(value string) error {
	v, err := strconv.ParseBool(value)
	if err != nil {
		return err
	}
	*d.target = v
	*d.used = true
	return nil
}

// IsBoolFlag lets it be given without a value, like the flag it replaces.
func (d deprecatedAlias) IsBoolFlag() bool { return true }

// requireFlags checks the command line: what to copy, from where, to where.
func requireFlags(cfg config) error {
	switch {
	case cfg.bucket == "":
		return cli.Usagef("-bucket is required")
	case cfg.spaceID == "":
		return cli.Usagef("-space is required")
	case cfg.outDir == "":
		return cli.Usagef("-out is required")
	}
	return nil
}

// requireCredentials checks the environment. Named here because the
// alternative is worse than an error: without static credentials the S3 SDK
// goes looking for ambient ones, ending at the cloud metadata service
// (review-2026-10.md F5).
func requireCredentials(cfg config) error {
	switch {
	case strings.TrimSpace(cfg.accessKey) == "":
		return errors.New("S3_ACCESS_KEY_ID is not set: export the target's access key id")
	case strings.TrimSpace(cfg.secretKey) == "":
		return errors.New("S3_SECRET_ACCESS_KEY is not set: export the target's secret access key")
	}
	return nil
}

// location addresses the target for the repository copy.
func (cfg config) location() snapshot.Location {
	return snapshot.Location{
		Endpoint:        cfg.endpoint,
		Region:          cfg.region,
		Bucket:          cfg.bucket,
		Prefix:          cfg.prefix,
		AccessKeyID:     cfg.accessKey,
		SecretAccessKey: cfg.secretKey,
		DisableTLS:      cfg.plainHTTP,
	}
}

// s3Target is the production openTarget.
func s3Target(ctx context.Context, cfg config) (snapshot.StorageOpener, objstore.Store, error) {
	objects, err := objstore.NewS3(ctx, objstore.S3Config{
		Endpoint:        cfg.endpoint,
		Region:          cfg.region,
		Bucket:          cfg.bucket,
		AccessKeyID:     cfg.accessKey,
		SecretAccessKey: cfg.secretKey,
		UsePathStyle:    cfg.usePathStyle,
		DisableTLS:      cfg.plainHTTP,
	})
	if err != nil {
		return nil, nil, err
	}
	return s3repo.Opener{}, objects, nil
}

func extract(ctx context.Context, cfg config, open openTarget, stdout, stderr io.Writer) error {
	level := slog.LevelWarn
	if cfg.verbose {
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	repos, objects, err := open(ctx, cfg)
	if err != nil {
		return err
	}

	manifest, err := remote.Extract(ctx, remote.ExtractOptions{
		Repos:                repos,
		Objects:              objects,
		Location:             cfg.location(),
		SpaceID:              cfg.spaceID,
		OutDir:               cfg.outDir,
		AllowMissingEnvelope: cfg.allowMissingEnvelope,
		Logger:               logger,
	})
	if err != nil {
		return explain(err)
	}

	if cfg.verify {
		if err := takeout.Verify(ctx, cfg.outDir); err != nil {
			return explain(err)
		}
	}
	return summarize(stdout, cfg.outDir, manifest)
}

// summarize tells the administrator what was written and what to do next.
func summarize(stdout io.Writer, outDir string, manifest takeout.Manifest) error {
	var b strings.Builder
	fmt.Fprintf(&b, "take-out written to %s\n", outDir)
	fmt.Fprintf(&b, "  space:     %s\n", manifest.SpaceID)
	fmt.Fprintf(&b, "  blobs:     %d (%d bytes)\n", manifest.BlobCount, manifest.TotalBytes)
	if manifest.Envelope != nil {
		fmt.Fprintf(&b, "  envelope:  version %d, %s\n", manifest.Envelope.Version, manifest.Envelope.KDF)
	} else {
		b.WriteString("  envelope:  MISSING — this take-out cannot be decrypted on its own\n")
	}
	b.WriteString("\nHand this directory to the space's owner. They decrypt it with:\n")
	fmt.Fprintf(&b, "  decrypt -in %s -out <folder>\n", outDir)
	_, err := io.WriteString(stdout, b.String())
	return err
}

// explain turns a library error into operator-facing advice without adding
// internal detail.
func explain(err error) error {
	switch {
	case errors.Is(err, takeout.ErrNoEnvelope):
		return fmt.Errorf("%w\n"+
			"       the space has no published recovery envelope on this target.\n"+
			"       run a backup first, or pass -allow-missing-envelope to copy the\n"+
			"       ciphertext anyway (it will not be decryptable on its own)", err)
	case errors.Is(err, takeout.ErrCorrupt):
		return fmt.Errorf("%w\n"+
			"       the copy did not match its own checksums; re-run the take-out", err)
	default:
		return err
	}
}

const usage = `takeout — extract an encrypted backup from the S3 target (admin).

This tool moves ciphertext only. It cannot decrypt anything and never asks for
a recovery key; the space's owner does that offline with the "decrypt" command.

Usage:
  S3_ACCESS_KEY_ID=... S3_SECRET_ACCESS_KEY=... \
  takeout -endpoint buddy.example:3900 -bucket backups -space <space-id> -out ./takeout

Add -plain-http when the endpoint speaks HTTP without TLS (a self-hosted
Garage often does).

Exit status: 0 done, 1 the take-out failed (for example the store could not be
read, or credentials are missing), 2 the command line was wrong.

Flags:
`
