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
	"syscall"

	"opencloud-backup-plugin/pkg/objstore"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/takeout"
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
	disableTLS   bool

	allowMissingEnvelope bool
	verify               bool
	verbose              bool
}

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "takeout: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, errOut *os.File) error {
	var cfg config
	fs := flags(&cfg, errOut)
	if err := fs.Parse(args); err != nil {
		return err
	}

	// S3 credentials come from the environment, not from flags: command lines
	// are visible to every process on the host and land in shell history.
	cfg.accessKey = os.Getenv("S3_ACCESS_KEY_ID")
	cfg.secretKey = os.Getenv("S3_SECRET_ACCESS_KEY")

	if err := validate(cfg); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return extract(ctx, cfg, errOut)
}

// flags defines the CLI's entire input surface. It is a separate function so a
// test can audit it: no flag here may ever accept key material.
func flags(cfg *config, errOut io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("takeout", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		_, _ = fmt.Fprint(errOut, usage)
		fs.PrintDefaults()
	}

	fs.StringVar(&cfg.endpoint, "endpoint", "", "S3 endpoint (host:port or URL)")
	fs.StringVar(&cfg.region, "region", "garage", "S3 region")
	fs.StringVar(&cfg.bucket, "bucket", "", "S3 bucket holding the backups")
	fs.StringVar(&cfg.prefix, "prefix", "", "deployment prefix inside the bucket")
	fs.StringVar(&cfg.spaceID, "space", "", "id of the space to extract")
	fs.StringVar(&cfg.outDir, "out", "", "directory to write the take-out to")
	fs.BoolVar(&cfg.usePathStyle, "path-style", true, "use path-style S3 addressing (required for Garage)")
	fs.BoolVar(&cfg.disableTLS, "insecure", false, "talk plain HTTP to the endpoint")
	fs.BoolVar(&cfg.allowMissingEnvelope, "allow-missing-envelope", false,
		"extract even if no recovery envelope is stored (the result cannot be decrypted on its own)")
	fs.BoolVar(&cfg.verify, "verify", true, "re-read the take-out and check it against its manifest")
	fs.BoolVar(&cfg.verbose, "v", false, "log progress")
	return fs
}

func validate(cfg config) error {
	switch {
	case cfg.bucket == "":
		return errors.New("-bucket is required")
	case cfg.spaceID == "":
		return errors.New("-space is required")
	case cfg.outDir == "":
		return errors.New("-out is required")
	}
	return nil
}

func extract(ctx context.Context, cfg config, errOut *os.File) error {
	level := slog.LevelWarn
	if cfg.verbose {
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewTextHandler(errOut, &slog.HandlerOptions{Level: level}))

	location := snapshot.Location{
		Endpoint:        cfg.endpoint,
		Region:          cfg.region,
		Bucket:          cfg.bucket,
		Prefix:          cfg.prefix,
		AccessKeyID:     cfg.accessKey,
		SecretAccessKey: cfg.secretKey,
		DisableTLS:      cfg.disableTLS,
	}

	objects, err := objstore.NewS3(ctx, objstore.S3Config{
		Endpoint:        cfg.endpoint,
		Region:          cfg.region,
		Bucket:          cfg.bucket,
		AccessKeyID:     cfg.accessKey,
		SecretAccessKey: cfg.secretKey,
		UsePathStyle:    cfg.usePathStyle,
		DisableTLS:      cfg.disableTLS,
	})
	if err != nil {
		return err
	}

	manifest, err := takeout.Extract(ctx, takeout.ExtractOptions{
		Repos:                snapshot.S3Opener{},
		Objects:              objects,
		Location:             location,
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

	fmt.Printf("take-out written to %s\n", cfg.outDir)
	fmt.Printf("  space:     %s\n", manifest.SpaceID)
	fmt.Printf("  blobs:     %d (%d bytes)\n", manifest.BlobCount, manifest.TotalBytes)
	if manifest.Envelope != nil {
		fmt.Printf("  envelope:  version %d, %s\n", manifest.Envelope.Version, manifest.Envelope.KDF)
	} else {
		fmt.Println("  envelope:  MISSING — this take-out cannot be decrypted on its own")
	}
	fmt.Println()
	fmt.Println("Hand this directory to the space's owner. They decrypt it with:")
	fmt.Printf("  decrypt -in %s -out <folder>\n", cfg.outDir)
	return nil
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

Flags:
`
