package takeout

// Path A end to end, without S3 and without OpenCloud: a repository is created
// in a local "bucket", taken out, and decrypted with the Recovery Key.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/objstore"
	"opencloud-backup-plugin/pkg/snapshot"
)

const (
	testSpaceID = "storage-1$space-1"
	testPrefix  = "oc/"
)

var testMTime = time.Date(2021, 3, 14, 15, 9, 26, 0, time.UTC)

// fixture is a seeded "bucket": one Space's repository plus its published
// recovery envelope.
type fixture struct {
	bucket  string
	store   objstore.Store
	dk      []byte
	rk      []byte
	rkBlob  []byte
	repo    snapshot.Repo
	engine  *snapshot.KopiaEngine
	snapIDs []snapshot.SnapshotID
	files   map[string]string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()

	bucket := t.TempDir()
	store := objstore.DirStore{Root: bucket}

	dk, err := keys.GenerateDK()
	if err != nil {
		t.Fatalf("GenerateDK: %v", err)
	}
	_, rk, err := keys.GenerateRecoveryKey()
	if err != nil {
		t.Fatalf("GenerateRecoveryKey: %v", err)
	}
	wrapped, err := keys.WrapWithRK(dk, rk, keys.DefaultArgonParams)
	if err != nil {
		t.Fatalf("WrapWithRK: %v", err)
	}
	if err := PublishTo(ctx, store, testPrefix, testSpaceID, wrapped.Blob); err != nil {
		t.Fatalf("PublishTo: %v", err)
	}

	engine, err := snapshot.NewEngine(
		snapshot.FilesystemOpener{Root: bucket},
		snapshot.EngineOptions{WorkDir: t.TempDir()},
	)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	repo := snapshot.Repo{
		Location: snapshot.Location{Prefix: testPrefix},
		Space:    snapshot.SpaceRef{SpaceID: testSpaceID},
		DK:       dk,
	}
	files := map[string]string{
		"readme.txt":     "top-secret plaintext ALPHA",
		"docs/notes.txt": "nested BRAVO",
		"фото/café.txt":  "unicode content",
	}
	info, err := engine.Snapshot(ctx, repo, newMemSource(files))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	return &fixture{
		bucket:  bucket,
		store:   store,
		dk:      dk,
		rk:      rk,
		rkBlob:  wrapped.Blob,
		repo:    repo,
		engine:  engine,
		snapIDs: []snapshot.SnapshotID{info.ID},
		files:   files,
	}
}

// extractOptions builds a Take-Out configuration against the fixture's bucket.
func (f *fixture) extractOptions(outDir string) ExtractOptions {
	return ExtractOptions{
		Repos:   snapshot.FilesystemOpener{Root: f.bucket},
		Objects: f.store,
		Location: snapshot.Location{
			Endpoint:        "garage:3900",
			Bucket:          "testbucket",
			Prefix:          testPrefix,
			AccessKeyID:     "GK-test-access-key",
			SecretAccessKey: "test-secret-access-key",
		},
		SpaceID: testSpaceID,
		OutDir:  outDir,
	}
}

// extract runs a Take-Out into a fresh directory.
func (f *fixture) extract(t *testing.T, opts ...func(*ExtractOptions)) (string, Manifest) {
	t.Helper()
	o := f.extractOptions(filepath.Join(t.TempDir(), "takeout"))
	for _, apply := range opts {
		apply(&o)
	}
	m, err := Extract(context.Background(), o)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return o.OutDir, m
}

func TestExtractProducesSelfContainedTakeOut(t *testing.T) {
	f := newFixture(t)
	dir, m := f.extract(t)

	if m.Format != ManifestFormat || m.Version != ManifestVersion {
		t.Fatalf("manifest header = %q v%d", m.Format, m.Version)
	}
	if m.SpaceID != testSpaceID {
		t.Fatalf("space id = %q", m.SpaceID)
	}
	if m.BlobCount == 0 || m.TotalBytes == 0 {
		t.Fatalf("manifest records nothing: %+v", m)
	}
	if m.Envelope == nil || m.Envelope.KDF != "argon2id" {
		t.Fatalf("envelope reference = %+v, want an argon2id RK envelope", m.Envelope)
	}
	// The manifest must describe the target it came from, without credentials.
	if m.Source.Bucket != "testbucket" || m.Source.RepoPrefix == "" {
		t.Fatalf("source reference = %+v", m.Source)
	}

	for _, name := range []string{ManifestFile, EnvelopeFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s missing from take-out: %v", name, err)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(dir, RepoDir)); err != nil || len(entries) == 0 {
		t.Fatalf("repository not copied: %v (%d entries)", err, len(entries))
	}

	if err := Verify(context.Background(), dir); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// The manifest is handed to an end user; target credentials must never be in it.
func TestManifestCarriesNoCredentials(t *testing.T) {
	f := newFixture(t)
	dir, _ := f.extract(t)

	body, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	for _, secret := range []string{"GK-test-access-key", "test-secret-access-key"} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatalf("manifest leaked target credential %q", secret)
		}
	}
}

// The Take-Out carries ciphertext only: no file name and no file content from
// the Space may be findable anywhere in it, envelope and manifest included.
func TestExtractCarriesNoPlaintext(t *testing.T) {
	f := newFixture(t)
	dir, _ := f.extract(t)

	markers := []string{"top-secret plaintext ALPHA", "nested BRAVO", "unicode content", "café", "notes.txt"}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, marker := range markers {
			if bytes.Contains(body, []byte(marker)) {
				t.Fatalf("plaintext marker %q leaked into %s", marker, p)
			}
		}
		if strings.Contains(p, "café") || strings.Contains(p, "notes.txt") {
			t.Fatalf("source name leaked into take-out path %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func TestExtractRequiresAnEnvelope(t *testing.T) {
	f := newFixture(t)

	// Simulate a target whose envelope was never published.
	if err := os.Remove(filepath.Join(f.bucket, snapshot.EnvelopeKey(
		snapshot.Location{Prefix: testPrefix}, snapshot.SpaceRef{SpaceID: testSpaceID}))); err != nil {
		t.Fatalf("remove envelope: %v", err)
	}

	opts := f.extractOptions(filepath.Join(t.TempDir(), "takeout"))
	if _, err := Extract(context.Background(), opts); !errors.Is(err, ErrNoEnvelope) {
		t.Fatalf("err = %v, want ErrNoEnvelope", err)
	}

	// Explicit opt-in still produces a (non-decryptable) copy.
	opts.AllowMissingEnvelope = true
	m, err := Extract(context.Background(), opts)
	if err != nil {
		t.Fatalf("Extract(AllowMissingEnvelope): %v", err)
	}
	if m.Envelope != nil {
		t.Fatalf("manifest claims an envelope that does not exist: %+v", m.Envelope)
	}

	// ... and decrypting it fails for the honest reason.
	if _, err := Decrypt(context.Background(), DecryptOptions{
		Dir: opts.OutDir, RecoveryKey: f.rk, OutDir: filepath.Join(t.TempDir(), "out"),
	}); !errors.Is(err, ErrNoEnvelope) {
		t.Fatalf("err = %v, want ErrNoEnvelope", err)
	}
}

func TestExtractValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	noOpener := f.extractOptions(t.TempDir())
	noOpener.Repos = nil
	noSpace := f.extractOptions(t.TempDir())
	noSpace.SpaceID = ""
	noOut := f.extractOptions("")

	for name, opts := range map[string]ExtractOptions{
		"no opener":  noOpener,
		"no space":   noSpace,
		"no out dir": noOut,
	} {
		if _, err := Extract(ctx, opts); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}

	unknown := f.extractOptions(filepath.Join(t.TempDir(), "takeout"))
	unknown.SpaceID = "no-such-space"
	unknown.AllowMissingEnvelope = true
	if _, err := Extract(ctx, unknown); err == nil {
		t.Error("extracting an unknown space must fail")
	}
}

func TestDecryptRestoresTheSnapshot(t *testing.T) {
	f := newFixture(t)
	dir, _ := f.extract(t)

	out := filepath.Join(t.TempDir(), "restored")
	res, err := Decrypt(context.Background(), DecryptOptions{
		Dir: dir, RecoveryKey: f.rk, OutDir: out, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if res.SpaceID != testSpaceID || res.SnapshotID != string(f.snapIDs[0]) {
		t.Fatalf("result = %+v", res)
	}

	for rel, want := range f.files {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", rel, got, want)
		}
	}

	fi, err := os.Stat(filepath.Join(out, "readme.txt"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !fi.ModTime().Truncate(time.Second).Equal(testMTime.Truncate(time.Second)) {
		t.Fatalf("mtime = %v, want %v", fi.ModTime(), testMTime)
	}
}

// A wrong Recovery Key must fail cleanly and leave no partial plaintext behind.
func TestDecryptWithWrongRecoveryKeyLeavesNothing(t *testing.T) {
	f := newFixture(t)
	dir, _ := f.extract(t)

	_, wrong, err := keys.GenerateRecoveryKey()
	if err != nil {
		t.Fatalf("GenerateRecoveryKey: %v", err)
	}

	out := filepath.Join(t.TempDir(), "restored")
	if _, err := Decrypt(context.Background(), DecryptOptions{
		Dir: dir, RecoveryKey: wrong, OutDir: out, WorkDir: t.TempDir(),
	}); !errors.Is(err, ErrWrongRecoveryKey) {
		t.Fatalf("err = %v, want ErrWrongRecoveryKey", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output directory exists after a failed decrypt: %v", err)
	}
}

// The failure message must not describe key material or hint at the cause.
func TestDecryptErrorsCarryNoKeyMaterial(t *testing.T) {
	f := newFixture(t)
	dir, _ := f.extract(t)

	_, wrong, err := keys.GenerateRecoveryKey()
	if err != nil {
		t.Fatalf("GenerateRecoveryKey: %v", err)
	}
	_, err = Decrypt(context.Background(), DecryptOptions{
		Dir: dir, RecoveryKey: wrong, OutDir: filepath.Join(t.TempDir(), "out"), WorkDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	for _, secret := range [][]byte{wrong, f.rk, f.dk} {
		if strings.Contains(msg, string(secret)) {
			t.Fatalf("error message leaked key material: %q", msg)
		}
	}
}

func TestDecryptSelectsSnapshot(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	// A second snapshot with changed content.
	files := map[string]string{"readme.txt": "second version"}
	info, err := f.engine.Snapshot(ctx, f.repo, newMemSource(files))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	dir, _ := f.extract(t)

	snaps, err := ListSnapshots(ctx, dir, f.rk, t.TempDir())
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("snapshots = %+v, want 2", snaps)
	}
	if snaps[0].ID != string(info.ID) {
		t.Fatalf("newest snapshot = %s, want %s", snaps[0].ID, info.ID)
	}

	// Default: newest.
	out := filepath.Join(t.TempDir(), "newest")
	if _, err := Decrypt(ctx, DecryptOptions{Dir: dir, RecoveryKey: f.rk, OutDir: out, WorkDir: t.TempDir()}); err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	assertContent(t, filepath.Join(out, "readme.txt"), "second version")

	// Explicit older snapshot.
	older := filepath.Join(t.TempDir(), "older")
	if _, err := Decrypt(ctx, DecryptOptions{
		Dir: dir, RecoveryKey: f.rk, OutDir: older, SnapshotID: string(f.snapIDs[0]), WorkDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("Decrypt(older): %v", err)
	}
	assertContent(t, filepath.Join(older, "readme.txt"), f.files["readme.txt"])

	// Unknown snapshot id.
	if _, err := Decrypt(ctx, DecryptOptions{
		Dir: dir, RecoveryKey: f.rk, OutDir: t.TempDir(), SnapshotID: "nope", WorkDir: t.TempDir(),
	}); !errors.Is(err, snapshot.ErrSnapshotNotFound) {
		t.Fatalf("err = %v, want ErrSnapshotNotFound", err)
	}
}

func TestDecryptRejectsDamagedTakeOut(t *testing.T) {
	f := newFixture(t)
	dir, _ := f.extract(t)

	if err := os.WriteFile(filepath.Join(dir, EnvelopeFile), []byte("not an envelope"), 0o600); err != nil {
		t.Fatalf("corrupt envelope: %v", err)
	}
	if _, err := Decrypt(context.Background(), DecryptOptions{
		Dir: dir, RecoveryKey: f.rk, OutDir: t.TempDir(), WorkDir: t.TempDir(),
	}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

// An envelope written by a future version must produce an actionable message:
// "get a newer tool", not "wrong key".
func TestDecryptRejectsFutureEnvelopeVersion(t *testing.T) {
	f := newFixture(t)
	dir, _ := f.extract(t)

	blob, err := os.ReadFile(filepath.Join(dir, EnvelopeFile))
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	blob[5] = byte(keys.EnvelopeVersion + 1)
	if err := os.WriteFile(filepath.Join(dir, EnvelopeFile), blob, 0o600); err != nil {
		t.Fatalf("write envelope: %v", err)
	}

	if _, err := Decrypt(context.Background(), DecryptOptions{
		Dir: dir, RecoveryKey: f.rk, OutDir: t.TempDir(), WorkDir: t.TempDir(),
	}); !errors.Is(err, ErrUnsupportedEnvelope) {
		t.Fatalf("err = %v, want ErrUnsupportedEnvelope", err)
	}
}

func TestDecryptRejectsNonTakeOutDirectory(t *testing.T) {
	if _, err := Decrypt(context.Background(), DecryptOptions{
		Dir: t.TempDir(), RecoveryKey: []byte("x"), OutDir: t.TempDir(),
	}); !errors.Is(err, ErrNoTakeOut) {
		t.Fatalf("err = %v, want ErrNoTakeOut", err)
	}
}

func TestDecryptRequiresRecoveryKeyAndOutput(t *testing.T) {
	f := newFixture(t)
	dir, _ := f.extract(t)

	if _, err := Decrypt(context.Background(), DecryptOptions{Dir: dir, RecoveryKey: f.rk}); err == nil {
		t.Fatal("missing output directory must be rejected")
	}
	if _, err := Decrypt(context.Background(), DecryptOptions{
		Dir: dir, OutDir: filepath.Join(t.TempDir(), "out"),
	}); err == nil {
		t.Fatal("missing recovery key must be rejected")
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	f := newFixture(t)
	dir, _ := f.extract(t)

	// Overwrite the first blob file kopia wrote, whatever it sharded it to.
	tamperOneBlob(t, filepath.Join(dir, RepoDir))

	if err := Verify(context.Background(), dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

// tamperOneBlob corrupts exactly one blob file inside a copied repository.
func tamperOneBlob(t *testing.T, repoDir string) {
	t.Helper()
	err := filepath.Walk(repoDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".f") {
			return err
		}
		if err := os.WriteFile(p, []byte("tampered"), 0o600); err != nil {
			return err
		}
		return io.EOF // stop after the first blob
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("no blob file found to tamper with: %v", err)
	}
}

func TestReadManifestRejectsForeignDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(`{"format":"something-else"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReadManifest(dir); !errors.Is(err, ErrNoTakeOut) {
		t.Fatalf("err = %v, want ErrNoTakeOut", err)
	}
}

func TestPublishToFilesEachEnvelopeByItsKind(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := objstore.DirStore{Root: root}

	dk, err := keys.GenerateDK()
	if err != nil {
		t.Fatalf("GenerateDK: %v", err)
	}
	_, rk, err := keys.GenerateRecoveryKey()
	if err != nil {
		t.Fatalf("GenerateRecoveryKey: %v", err)
	}
	wrapped, err := keys.WrapWithRK(dk, rk, keys.DefaultArgonParams)
	if err != nil {
		t.Fatalf("WrapWithRK: %v", err)
	}

	key := snapshot.EnvelopeKey(snapshot.Location{Prefix: testPrefix}, snapshot.SpaceRef{SpaceID: testSpaceID})
	if err := PublishTo(ctx, store, testPrefix, testSpaceID, wrapped.Blob); err != nil {
		t.Fatalf("PublishTo: %v", err)
	}
	firstMod := modTime(t, filepath.Join(root, key))

	// Republishing identical bytes must not rewrite the object.
	if err := PublishTo(ctx, store, testPrefix, testSpaceID, wrapped.Blob); err != nil {
		t.Fatalf("PublishTo (again): %v", err)
	}
	if got := modTime(t, filepath.Join(root, key)); !got.Equal(firstMod) {
		t.Fatal("an unchanged envelope was rewritten")
	}

	// The server envelope is published too, but never over the user's only
	// recovery path: the object name comes from the envelope's own header.
	srwKey, err := keys.GenerateSRWKey()
	if err != nil {
		t.Fatalf("GenerateSRWKey: %v", err)
	}
	srw, err := keys.WrapWithSRW(dk, srwKey)
	if err != nil {
		t.Fatalf("WrapWithSRW: %v", err)
	}
	if err := PublishTo(ctx, store, testPrefix, testSpaceID, srw.Blob); err != nil {
		t.Fatalf("PublishTo (server envelope): %v", err)
	}

	serverKey := snapshot.ServerEnvelopeKey(snapshot.Location{Prefix: testPrefix}, snapshot.SpaceRef{SpaceID: testSpaceID})
	if serverKey == key {
		t.Fatal("the server envelope must not share the recovery envelope's object name")
	}
	if got := readObject(t, store, key); !bytes.Equal(got, wrapped.Blob) {
		t.Fatal("the recovery envelope was overwritten by the server envelope")
	}
	if got := readObject(t, store, serverKey); !bytes.Equal(got, srw.Blob) {
		t.Fatal("the server envelope was not stored under its own object name")
	}

	// Anything that is not a Data Key envelope is still refused outright.
	if err := PublishTo(ctx, store, testPrefix, testSpaceID, []byte("garbage")); err == nil {
		t.Fatal("publishing a non-envelope must be refused")
	}
}

// readObject reads one published object.
func readObject(t *testing.T, store objstore.Store, key string) []byte {
	t.Helper()
	rc, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get %s: %v", key, err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return data
}

func TestPublishToValidation(t *testing.T) {
	ctx := context.Background()
	if err := PublishTo(ctx, nil, testPrefix, testSpaceID, nil); err == nil {
		t.Fatal("a nil store must be rejected")
	}
	if err := PublishTo(ctx, objstore.DirStore{Root: t.TempDir()}, testPrefix, "", nil); err == nil {
		t.Fatal("an empty space id must be rejected")
	}
}

// --- helpers ---------------------------------------------------------------

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.ModTime()
}

// memSource is a trivial snapshot.Source over a path->content map.
type memSource struct{ files map[string]string }

func newMemSource(files map[string]string) *memSource { return &memSource{files: files} }

func (m *memSource) Name() string { return testSpaceID }

func (m *memSource) List(_ context.Context, dir string) ([]snapshot.Node, error) {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	seen := map[string]bool{}
	var out []snapshot.Node
	for p, content := range m.files {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := strings.TrimPrefix(p, prefix)
		if i := strings.Index(rest, "/"); i >= 0 {
			name := rest[:i]
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, snapshot.Node{Name: name, IsDir: true, ModTime: testMTime})
			continue
		}
		out = append(out, snapshot.Node{Name: rest, Size: int64(len(content)), ModTime: testMTime})
	}
	return out, nil
}

func (m *memSource) Open(_ context.Context, p string, offset int64) (io.ReadCloser, error) {
	content, ok := m.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(strings.NewReader(content[offset:])), nil
}
