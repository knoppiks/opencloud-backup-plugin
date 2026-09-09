//go:build integration

package backup

// Phase-5 acceptance test for restore Path A (success metric 3):
//
//	backup to Garage -> OpenCloud unavailable -> admin take-out -> user decrypt
//
// "OpenCloud is down" is enforced, not assumed: before the take-out runs, the
// CS3 reader is broken so *any* call into OpenCloud fails the test. Neither
// `takeout` nor `decrypt` may touch it.
//
// Run: go test -tags integration -run TestIntegration_PathA ./pkg/backup/...

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/objstore"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/takeout"
	takeoutdecrypt "opencloud-backup-plugin/pkg/takeout/decrypt"
)

// garageObjects opens a plain object-store client against the fixture, as the
// takeout CLI does.
func garageObjects(ctx context.Context, t *testing.T, p *garagePipeline) objstore.Store {
	t.Helper()
	store, err := objstore.NewS3(ctx, objstore.S3Config{
		Endpoint:        p.garage.Endpoint,
		Region:          p.garage.Region,
		Bucket:          p.garage.Bucket,
		AccessKeyID:     p.garage.AccessKeyID,
		SecretAccessKey: p.garage.SecretAccessKey,
		UsePathStyle:    true,
		DisableTLS:      true,
	})
	if err != nil {
		t.Fatalf("objstore.NewS3: %v", err)
	}
	return store
}

// stopOpenCloud makes every CS3 call fail, standing in for a dead deployment.
func stopOpenCloud(p *garagePipeline) {
	p.reader.listSpacesErr = errors.New("opencloud is down")
	p.reader.failOpen("readme.txt", errors.New("opencloud is down"))
	p.reader.failOpen("docs/notes.txt", errors.New("opencloud is down"))
	p.reader.failOpen("фото/café.txt", errors.New("opencloud is down"))
	p.reader.failOpen("assets/große-datei.bin", errors.New("opencloud is down"))
}

func TestIntegration_PathA_TakeOutAndDecryptWithOpenCloudDown(t *testing.T) {
	ctx := context.Background()
	p := newGaragePipeline(ctx, t)

	if _, err := p.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}

	// The envelope must be on the target, outside the kopia repo prefix, or a
	// take-out could never be self-contained.
	envKey := snapshot.EnvelopeKey(p.repo.Location, p.repo.Space)
	if strings.HasPrefix(envKey, snapshot.RepoPrefix(p.repo.Location, p.repo.Space)) {
		t.Fatalf("envelope key %q is inside the repository prefix", envKey)
	}
	objects := garageObjects(ctx, t, p)
	if _, err := objects.Get(ctx, envKey); err != nil {
		t.Fatalf("recovery envelope was not published to the target: %v", err)
	}

	// --- the deployment goes away -----------------------------------------
	stopOpenCloud(p)

	// --- admin take-out (ciphertext only, no key input) --------------------
	takeoutDir := filepath.Join(t.TempDir(), "takeout")
	manifest, err := takeout.Extract(ctx, takeout.ExtractOptions{
		Repos:    snapshot.S3Opener{},
		Objects:  objects,
		Location: p.repo.Location,
		SpaceID:  testSpaceID,
		OutDir:   takeoutDir,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if manifest.BlobCount == 0 || manifest.Envelope == nil {
		t.Fatalf("manifest = %+v", manifest)
	}
	if err := takeout.Verify(ctx, takeoutDir); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// The take-out is handed to a user: it must carry no plaintext and no
	// target credentials.
	assertTakeOutCarriesNoPlaintext(t, takeoutDir, p)

	// --- user decrypt, offline --------------------------------------------
	out := filepath.Join(t.TempDir(), "restored")
	res, err := takeoutdecrypt.Decrypt(ctx, takeoutdecrypt.Options{
		Dir:         takeoutDir,
		RecoveryKey: p.rk,
		OutDir:      out,
		WorkDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if res.SpaceID != testSpaceID {
		t.Fatalf("result = %+v", res)
	}

	// Byte-identical, including the 24 MiB multipart file and unicode paths.
	for rel, want := range map[string]string{
		"readme.txt":     "top-secret plaintext marker ALPHA",
		"docs/notes.txt": "nested marker BRAVO with more text",
		"фото/café.txt":  "unicode path content",
	} {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", rel, got, want)
		}
	}
	gotBig, err := os.ReadFile(filepath.Join(out, "assets", "große-datei.bin"))
	if err != nil {
		t.Fatalf("read large file: %v", err)
	}
	if !bytes.Equal(gotBig, p.bigFile) {
		t.Fatalf("large file differs: %d vs %d bytes", len(gotBig), len(p.bigFile))
	}

	// mtimes are in backup scope and must survive the whole path.
	fi, err := os.Stat(filepath.Join(out, "readme.txt"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !fi.ModTime().Truncate(time.Second).Equal(testMTime.Truncate(time.Second)) {
		t.Fatalf("mtime = %v, want %v", fi.ModTime(), testMTime)
	}
}

// A wrong Recovery Key must fail cleanly and leave no partial plaintext behind.
func TestIntegration_PathA_WrongRecoveryKeyLeavesNothing(t *testing.T) {
	ctx := context.Background()
	p := newGaragePipeline(ctx, t)

	if _, err := p.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}
	stopOpenCloud(p)

	takeoutDir := filepath.Join(t.TempDir(), "takeout")
	if _, err := takeout.Extract(ctx, takeout.ExtractOptions{
		Repos:    snapshot.S3Opener{},
		Objects:  garageObjects(ctx, t, p),
		Location: p.repo.Location,
		SpaceID:  testSpaceID,
		OutDir:   takeoutDir,
	}); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	_, wrong, err := keys.GenerateRecoveryKey()
	if err != nil {
		t.Fatalf("GenerateRecoveryKey: %v", err)
	}

	out := filepath.Join(t.TempDir(), "restored")
	_, err = takeoutdecrypt.Decrypt(ctx, takeoutdecrypt.Options{
		Dir: takeoutDir, RecoveryKey: wrong, OutDir: out, WorkDir: t.TempDir(),
	})
	if !errors.Is(err, takeoutdecrypt.ErrWrongRecoveryKey) {
		t.Fatalf("err = %v, want ErrWrongRecoveryKey", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed decrypt left an output directory behind: %v", err)
	}
}

// A take-out of a Space whose envelope was never published must fail loudly
// rather than produce something the user cannot open.
func TestIntegration_PathA_RefusesTakeOutWithoutEnvelope(t *testing.T) {
	ctx := context.Background()
	p := newGaragePipeline(ctx, t)

	if _, err := p.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}

	// Simulate a target whose envelope is absent by extracting a different
	// space id: its repository exists only in this test's imagination, so use
	// the real repo prefix but a store that cannot see the envelope.
	empty := objstore.DirStore{Root: t.TempDir()}
	_, err := takeout.Extract(ctx, takeout.ExtractOptions{
		Repos:    snapshot.S3Opener{},
		Objects:  empty,
		Location: p.repo.Location,
		SpaceID:  testSpaceID,
		OutDir:   filepath.Join(t.TempDir(), "takeout"),
	})
	if !errors.Is(err, takeout.ErrNoEnvelope) {
		t.Fatalf("err = %v, want ErrNoEnvelope", err)
	}
}

// assertTakeOutCarriesNoPlaintext scans every file in a take-out for source
// names, file contents, and the target's credentials.
func assertTakeOutCarriesNoPlaintext(t *testing.T, dir string, p *garagePipeline) {
	t.Helper()

	secrets := [][]byte{
		[]byte(p.garage.AccessKeyID),
		[]byte(p.garage.SecretAccessKey),
	}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		for _, name := range nameMarkers {
			if strings.Contains(path, name) {
				t.Fatalf("source name %q leaked into take-out path %s", name, path)
			}
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, marker := range contentMarkers {
			if bytes.Contains(body, marker) {
				t.Fatalf("plaintext marker %q leaked into %s", marker, path)
			}
		}
		for _, secret := range secrets {
			if len(secret) > 0 && bytes.Contains(body, secret) {
				t.Fatalf("target credential leaked into %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk take-out: %v", err)
	}
}
