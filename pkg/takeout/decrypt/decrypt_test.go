package decrypt

// The user-side half of Path A, without S3 and without OpenCloud: a repository
// is created in a local "bucket", taken out, and decrypted with the Recovery
// Key.

import (
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
	"opencloud-backup-plugin/pkg/takeout"
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
	dk      []byte
	rk      []byte
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
	if err := takeout.PublishTo(ctx, store, testPrefix, testSpaceID, wrapped.Blob); err != nil {
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
		dk:      dk,
		rk:      rk,
		repo:    repo,
		engine:  engine,
		snapIDs: []snapshot.SnapshotID{info.ID},
		files:   files,
	}
}

// extract runs a Take-Out into a fresh directory.
func (f *fixture) extract(t *testing.T) string {
	t.Helper()
	outDir := filepath.Join(t.TempDir(), "takeout")
	if _, err := takeout.Extract(context.Background(), takeout.ExtractOptions{
		Repos:    snapshot.FilesystemOpener{Root: f.bucket},
		Objects:  objstore.DirStore{Root: f.bucket},
		Location: snapshot.Location{Prefix: testPrefix},
		SpaceID:  testSpaceID,
		OutDir:   outDir,
	}); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return outDir
}

func TestDecryptRestoresTheSnapshot(t *testing.T) {
	f := newFixture(t)
	dir := f.extract(t)

	out := filepath.Join(t.TempDir(), "restored")
	res, err := Decrypt(context.Background(), Options{
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
	dir := f.extract(t)

	_, wrong, err := keys.GenerateRecoveryKey()
	if err != nil {
		t.Fatalf("GenerateRecoveryKey: %v", err)
	}

	out := filepath.Join(t.TempDir(), "restored")
	if _, err := Decrypt(context.Background(), Options{
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
	dir := f.extract(t)

	_, wrong, err := keys.GenerateRecoveryKey()
	if err != nil {
		t.Fatalf("GenerateRecoveryKey: %v", err)
	}
	_, err = Decrypt(context.Background(), Options{
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
	dir := f.extract(t)

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
	if _, err := Decrypt(ctx, Options{Dir: dir, RecoveryKey: f.rk, OutDir: out, WorkDir: t.TempDir()}); err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	assertContent(t, filepath.Join(out, "readme.txt"), "second version")

	// Explicit older snapshot.
	older := filepath.Join(t.TempDir(), "older")
	if _, err := Decrypt(ctx, Options{
		Dir: dir, RecoveryKey: f.rk, OutDir: older, SnapshotID: string(f.snapIDs[0]), WorkDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("Decrypt(older): %v", err)
	}
	assertContent(t, filepath.Join(older, "readme.txt"), f.files["readme.txt"])

	// Unknown snapshot id.
	if _, err := Decrypt(ctx, Options{
		Dir: dir, RecoveryKey: f.rk, OutDir: t.TempDir(), SnapshotID: "nope", WorkDir: t.TempDir(),
	}); !errors.Is(err, snapshot.ErrSnapshotNotFound) {
		t.Fatalf("err = %v, want ErrSnapshotNotFound", err)
	}
}

func TestDecryptRejectsDamagedTakeOut(t *testing.T) {
	f := newFixture(t)
	dir := f.extract(t)

	if err := os.WriteFile(filepath.Join(dir, takeout.EnvelopeFile), []byte("not an envelope"), 0o600); err != nil {
		t.Fatalf("corrupt envelope: %v", err)
	}
	if _, err := Decrypt(context.Background(), Options{
		Dir: dir, RecoveryKey: f.rk, OutDir: t.TempDir(), WorkDir: t.TempDir(),
	}); !errors.Is(err, takeout.ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

// An envelope written by a future version must produce an actionable message:
// "get a newer tool", not "wrong key".
func TestDecryptRejectsFutureEnvelopeVersion(t *testing.T) {
	f := newFixture(t)
	dir := f.extract(t)

	blob, err := os.ReadFile(filepath.Join(dir, takeout.EnvelopeFile))
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	blob[5] = byte(keys.EnvelopeVersion + 1)
	if err := os.WriteFile(filepath.Join(dir, takeout.EnvelopeFile), blob, 0o600); err != nil {
		t.Fatalf("write envelope: %v", err)
	}

	if _, err := Decrypt(context.Background(), Options{
		Dir: dir, RecoveryKey: f.rk, OutDir: t.TempDir(), WorkDir: t.TempDir(),
	}); !errors.Is(err, ErrUnsupportedEnvelope) {
		t.Fatalf("err = %v, want ErrUnsupportedEnvelope", err)
	}
}

// A Take-Out taken without an envelope fails for the honest reason.
func TestDecryptWithoutAnEnvelope(t *testing.T) {
	f := newFixture(t)
	if err := os.Remove(filepath.Join(f.bucket, snapshot.EnvelopeKey(
		snapshot.Location{Prefix: testPrefix}, snapshot.SpaceRef{SpaceID: testSpaceID}))); err != nil {
		t.Fatalf("remove envelope: %v", err)
	}

	outDir := filepath.Join(t.TempDir(), "takeout")
	if _, err := takeout.Extract(context.Background(), takeout.ExtractOptions{
		Repos:                snapshot.FilesystemOpener{Root: f.bucket},
		Objects:              objstore.DirStore{Root: f.bucket},
		Location:             snapshot.Location{Prefix: testPrefix},
		SpaceID:              testSpaceID,
		OutDir:               outDir,
		AllowMissingEnvelope: true,
	}); err != nil {
		t.Fatalf("Extract(AllowMissingEnvelope): %v", err)
	}

	if _, err := Decrypt(context.Background(), Options{
		Dir: outDir, RecoveryKey: f.rk, OutDir: filepath.Join(t.TempDir(), "out"),
	}); !errors.Is(err, takeout.ErrNoEnvelope) {
		t.Fatalf("err = %v, want ErrNoEnvelope", err)
	}
}

func TestDecryptRejectsNonTakeOutDirectory(t *testing.T) {
	if _, err := Decrypt(context.Background(), Options{
		Dir: t.TempDir(), RecoveryKey: []byte("x"), OutDir: t.TempDir(),
	}); !errors.Is(err, takeout.ErrNoTakeOut) {
		t.Fatalf("err = %v, want ErrNoTakeOut", err)
	}
}

func TestDecryptRequiresRecoveryKeyAndOutput(t *testing.T) {
	f := newFixture(t)
	dir := f.extract(t)

	if _, err := Decrypt(context.Background(), Options{Dir: dir, RecoveryKey: f.rk}); err == nil {
		t.Fatal("missing output directory must be rejected")
	}
	if _, err := Decrypt(context.Background(), Options{
		Dir: dir, OutDir: filepath.Join(t.TempDir(), "out"),
	}); err == nil {
		t.Fatal("missing recovery key must be rejected")
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
