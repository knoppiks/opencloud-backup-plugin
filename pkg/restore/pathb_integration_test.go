//go:build integration

package restore_test

// Phase-5 integration suite for restore Path B (success metric 4), against a
// real kopia repository on an ephemeral Garage:
//
//	seeded Space -> backup -> restore into Restore/<ts>/ -> verify
//
// It also checks the round-trip property the phase plan asks for: backing up
// again after a restore adds almost nothing to the target, because the restored
// copy deduplicates against the data it came from. Dedup is the cheapest proof
// that the restore reproduced the bytes exactly.
//
// Run: go test -tags integration ./pkg/restore/...

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/backup"
	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/restore"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/spacecfg"
	"opencloud-backup-plugin/pkg/targets"
)

const (
	spaceID  = "storage-1$space-1"
	targetID = "target-1"
	prefix   = "oc/"
)

var mtime = time.Date(2021, 3, 14, 15, 9, 26, 0, time.UTC)

// --- an in-memory Space that can be both read and written -------------------

// memSpace implements cs3.SpaceReader and cs3.SpaceWriter over one flat map, so
// a restore's output becomes part of the Space and the next backup sees it.
type memSpace struct {
	space cs3.Space

	mu    sync.Mutex
	files map[string]memFile
	dirs  map[string]bool
}

type memFile struct {
	data    []byte
	modTime time.Time
}

func newMemSpace() *memSpace {
	return &memSpace{
		space: cs3.Space{
			ID:    spaceID,
			Name:  "Alice",
			Type:  "personal",
			Owner: "alice",
			Root:  cs3.ResourceID{StorageID: "storage-1", SpaceID: "space-1", OpaqueID: "root-1"},
		},
		files: map[string]memFile{},
		dirs:  map[string]bool{},
	}
}

func (m *memSpace) put(p string, data []byte, modTime time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[p] = memFile{data: data, modTime: modTime}
}

func (m *memSpace) snapshotOfPaths() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.files))
	for p := range m.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (m *memSpace) file(p string) (memFile, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[p]
	return f, ok
}

func (m *memSpace) ListSpaces(context.Context) ([]cs3.Space, error) {
	return []cs3.Space{m.space}, nil
}

func (m *memSpace) ListDir(_ context.Context, _ cs3.Space, dir string) ([]cs3.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	p := ""
	if dir != "" {
		p = dir + "/"
	}
	seen := map[string]bool{}
	var entries []cs3.Entry
	for full, f := range m.files {
		if !strings.HasPrefix(full, p) {
			continue
		}
		rest := strings.TrimPrefix(full, p)
		if i := strings.Index(rest, "/"); i >= 0 {
			name := rest[:i]
			if seen[name] {
				continue
			}
			seen[name] = true
			entries = append(entries, cs3.Entry{Path: p + name, IsDir: true, MTimeUnix: mtime.Unix()})
			continue
		}
		entries = append(entries, cs3.Entry{
			Path:      full,
			Size:      int64(len(f.data)),
			MTimeUnix: f.modTime.Unix(),
		})
	}
	// Empty directories that only exist because a restore created them.
	for d := range m.dirs {
		if !strings.HasPrefix(d, p) {
			continue
		}
		rest := strings.TrimPrefix(d, p)
		if rest == "" || strings.Contains(rest, "/") || seen[rest] {
			continue
		}
		seen[rest] = true
		entries = append(entries, cs3.Entry{Path: d, IsDir: true, MTimeUnix: mtime.Unix()})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func (m *memSpace) Walk(ctx context.Context, space cs3.Space, fn func(cs3.Entry) error) error {
	var walk func(string) error
	walk = func(dir string) error {
		entries, err := m.ListDir(ctx, space, dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := fn(e); err != nil {
				return err
			}
			if e.IsDir {
				if err := walk(e.Path); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk("")
}

func (m *memSpace) OpenFile(_ context.Context, _ cs3.Space, p string, offset int64) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[p]
	if !ok {
		return nil, fmt.Errorf("memSpace: no file %q", p)
	}
	return io.NopCloser(bytes.NewReader(f.data[offset:])), nil
}

func (m *memSpace) MakeDir(_ context.Context, _ cs3.Space, relDir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if relDir != "" {
		m.dirs[relDir] = true
	}
	return nil
}

func (m *memSpace) Upload(_ context.Context, _ cs3.Space, relPath string, size int64, modTime time.Time, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return fmt.Errorf("memSpace: announced %d bytes, got %d", size, len(data))
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.files[relPath]; exists {
		// A restore must never overwrite; surfacing this as an error makes an
		// accidental overwrite a test failure rather than silent data loss.
		return fmt.Errorf("%w: %s", cs3.ErrAlreadyExists, relPath)
	}
	m.files[relPath] = memFile{data: data, modTime: modTime}
	return nil
}

// Delete satisfies cs3.SpaceWriter. A restore never deletes anything, so
// reaching this is a bug worth failing on.
func (m *memSpace) Delete(_ context.Context, _ cs3.Space, relPath string) error {
	return fmt.Errorf("memSpace: restore attempted to delete %q", relPath)
}

// --- fixture ---------------------------------------------------------------

type fixture struct {
	space    *memSpace
	backup   *backup.Runner
	restore  *restore.Runner
	engine   *snapshot.KopiaEngine
	garage   *testutil.Garage
	jobs     *jobs.MemoryStore
	location snapshot.Location
	bigFile  []byte
}

func newFixture(ctx context.Context, t *testing.T) *fixture {
	t.Helper()

	garage := testutil.StartGarage(ctx, t)
	space := newMemSpace()
	space.put("readme.txt", []byte("hello from the space"), mtime)
	space.put("docs/notes.txt", []byte("nested notes"), mtime)
	space.put("фото/café.txt", []byte("unicode content"), mtime)

	// Above kopia's pack threshold: proves the streaming restore path handles
	// multi-blob files, not just tiny ones.
	big := make([]byte, 24<<20)
	if _, err := rand.Read(big); err != nil {
		t.Fatalf("rand: %v", err)
	}
	space.put("assets/big.bin", big, mtime)

	engine, err := snapshot.NewEngine(snapshot.S3Opener{}, snapshot.EngineOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	sealer := newSealer(t)
	wrapper := newSRWWrapper(t)
	configs := spacecfg.NewMemoryStore()
	targetStore := targets.NewMemoryStore()
	keyStore := keys.NewMemoryStore()
	jobStore := jobs.NewMemoryStore()

	location := snapshot.Location{
		Endpoint:        strings.TrimPrefix(garage.Endpoint, "http://"),
		Region:          garage.Region,
		Bucket:          garage.Bucket,
		Prefix:          prefix,
		AccessKeyID:     garage.AccessKeyID,
		SecretAccessKey: garage.SecretAccessKey,
		DisableTLS:      true,
	}
	seedTarget(t, targetStore, sealer, location)
	seedConfig(t, configs)
	seedKeys(t, keyStore, wrapper)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	backupRunner, err := backup.NewRunner(backup.Deps{
		Spaces: space, Configs: configs, Targets: targetStore, Sealer: sealer,
		Keys: keyStore, Unwrap: wrapper, Engine: engine, Jobs: jobStore, Locks: jobStore,
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("backup.NewRunner: %v", err)
	}

	restoreRunner, err := restore.NewRunner(restore.Deps{
		Spaces: space, Writer: space, Configs: configs, Targets: targetStore, Sealer: sealer,
		Keys: keyStore, Unwrap: wrapper, Engine: engine, Jobs: jobStore, Locks: jobStore,
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("restore.NewRunner: %v", err)
	}

	return &fixture{
		space: space, backup: backupRunner, restore: restoreRunner,
		engine: engine, garage: garage, jobs: jobStore, location: location, bigFile: big,
	}
}

func seedTarget(t *testing.T, store *targets.MemoryStore, sealer targets.CredSealer, loc snapshot.Location) {
	t.Helper()
	wrapped, version, err := sealer.Seal(targets.PlainCreds{
		AccessKeyID:     loc.AccessKeyID,
		SecretAccessKey: loc.SecretAccessKey,
	})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := store.CreateTarget(context.Background(), targets.Target{
		ID: targetID, Name: "Garage",
		Endpoint: loc.Endpoint, Region: loc.Region, Bucket: loc.Bucket, Prefix: loc.Prefix,
		UsePathStyle: true, DisableTLS: true,
		WrappedCreds: wrapped, Version: version,
	}); err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
}

func seedConfig(t *testing.T, store *spacecfg.MemoryStore) {
	t.Helper()
	if _, err := store.Put(context.Background(), spacecfg.Config{
		SpaceID: spaceID, TargetID: targetID, Enabled: true,
	}); err != nil {
		t.Fatalf("spacecfg.Put: %v", err)
	}
}

func seedKeys(t *testing.T, store *keys.MemoryStore, wrapper *keys.SRWWrapper) {
	t.Helper()
	dk, err := keys.GenerateDK()
	if err != nil {
		t.Fatalf("GenerateDK: %v", err)
	}
	defer keys.Zeroize(dk)

	wrapped, err := wrapper.WrapSRW(dk)
	if err != nil {
		t.Fatalf("WrapSRW: %v", err)
	}
	if err := store.PutSRW(spaceID, wrapped); err != nil {
		t.Fatalf("PutSRW: %v", err)
	}
}

func newSealer(t *testing.T) targets.CredSealer {
	t.Helper()
	twKey, err := keys.GenerateSRWKey()
	if err != nil {
		t.Fatalf("GenerateSRWKey: %v", err)
	}
	sealer, err := targets.NewCredSealer(twKey)
	if err != nil {
		t.Fatalf("NewCredSealer: %v", err)
	}
	return sealer
}

func newSRWWrapper(t *testing.T) *keys.SRWWrapper {
	t.Helper()
	srwKey, err := keys.GenerateSRWKey()
	if err != nil {
		t.Fatalf("GenerateSRWKey: %v", err)
	}
	w, err := keys.NewSRWWrapper(srwKey)
	if err != nil {
		t.Fatalf("NewSRWWrapper: %v", err)
	}
	t.Cleanup(w.Close)
	return w
}

// --- tests -----------------------------------------------------------------

func TestIntegration_PathB_RestoreLandsInRestoreFolder(t *testing.T) {
	ctx := context.Background()
	f := newFixture(ctx, t)

	res, err := f.backup.RunBackup(ctx, spaceID)
	if err != nil {
		t.Fatalf("RunBackup: %v", err)
	}
	before := f.space.snapshotOfPaths()

	restored, err := f.restore.RunRestore(ctx, spaceID, res.SnapshotID)
	if err != nil {
		t.Fatalf("RunRestore: %v", err)
	}
	if !strings.HasPrefix(restored.Folder, restore.RestoreFolder+"/") {
		t.Fatalf("restore folder = %q", restored.Folder)
	}

	// Every original file is still there, byte for byte: live data untouched.
	for _, p := range before {
		if _, ok := f.space.file(p); !ok {
			t.Fatalf("live file %q disappeared during restore", p)
		}
	}
	if got, _ := f.space.file("readme.txt"); string(got.data) != "hello from the space" {
		t.Fatalf("live file was modified: %q", got.data)
	}

	// The snapshot is reproduced under the restore folder.
	for rel, want := range map[string]string{
		"readme.txt":     "hello from the space",
		"docs/notes.txt": "nested notes",
		"фото/café.txt":  "unicode content",
	} {
		got, ok := f.space.file(path.Join(restored.Folder, rel))
		if !ok {
			t.Fatalf("%s was not restored; space holds %v", rel, f.space.snapshotOfPaths())
		}
		if string(got.data) != want {
			t.Fatalf("%s = %q, want %q", rel, got.data, want)
		}
		if !got.modTime.Truncate(time.Second).Equal(mtime.Truncate(time.Second)) {
			t.Fatalf("%s mtime = %v, want %v", rel, got.modTime, mtime)
		}
	}

	// A multi-blob file must stream back intact.
	gotBig, ok := f.space.file(path.Join(restored.Folder, "assets/big.bin"))
	if !ok {
		t.Fatal("large file was not restored")
	}
	if !bytes.Equal(gotBig.data, f.bigFile) {
		t.Fatalf("large file differs: %d vs %d bytes", len(gotBig.data), len(f.bigFile))
	}

	if restored.FileCount != int64(len(before)) {
		t.Fatalf("restored %d files, want %d", restored.FileCount, len(before))
	}
}

// The round-trip property from the phase plan: backup -> restore -> backup adds
// almost nothing to the target, because the restored copy deduplicates against
// the original. That is dedup proving restore fidelity.
func TestIntegration_PathB_RestoredCopyDeduplicates(t *testing.T) {
	ctx := context.Background()
	f := newFixture(ctx, t)

	first, err := f.backup.RunBackup(ctx, spaceID)
	if err != nil {
		t.Fatalf("first RunBackup: %v", err)
	}
	afterFirst := totalBytes(ctx, t, f)

	if _, err := f.restore.RunRestore(ctx, spaceID, first.SnapshotID); err != nil {
		t.Fatalf("RunRestore: %v", err)
	}

	// The Space now holds a second copy of everything, ~24 MiB of it.
	if _, err := f.backup.RunBackup(ctx, spaceID); err != nil {
		t.Fatalf("second RunBackup: %v", err)
	}
	growth := totalBytes(ctx, t, f) - afterFirst

	t.Logf("target grew by %d bytes after backing up a full restored copy", growth)
	if growth > 4<<20 {
		t.Fatalf("restored copy did not deduplicate: target grew by %d bytes", growth)
	}
}

func TestIntegration_PathB_ListSnapshots(t *testing.T) {
	ctx := context.Background()
	f := newFixture(ctx, t)

	if _, err := f.restore.ListSnapshots(ctx, spaceID); err == nil {
		// Before any backup the repository does not exist yet; listing must
		// fail rather than pretend there is nothing to restore.
		t.Fatal("listing snapshots of an uninitialised repository must fail")
	}

	first, err := f.backup.RunBackup(ctx, spaceID)
	if err != nil {
		t.Fatalf("RunBackup: %v", err)
	}
	f.space.put("readme.txt", []byte("hello from the space (v2)"), mtime.Add(time.Hour))
	second, err := f.backup.RunBackup(ctx, spaceID)
	if err != nil {
		t.Fatalf("second RunBackup: %v", err)
	}

	got, err := f.restore.ListSnapshots(ctx, spaceID)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("snapshots = %+v, want 2", got)
	}
	if got[0].ID != second.SnapshotID || got[1].ID != first.SnapshotID {
		t.Fatalf("snapshots are not newest-first: %+v", got)
	}

	// Restoring the older snapshot brings back the previous content, without
	// touching the live file.
	res, err := f.restore.RunRestore(ctx, spaceID, first.SnapshotID)
	if err != nil {
		t.Fatalf("RunRestore(older): %v", err)
	}
	old, ok := f.space.file(path.Join(res.Folder, "readme.txt"))
	if !ok || string(old.data) != "hello from the space" {
		t.Fatalf("older snapshot not restored: %q", old.data)
	}
	live, _ := f.space.file("readme.txt")
	if string(live.data) != "hello from the space (v2)" {
		t.Fatalf("live file was overwritten: %q", live.data)
	}
}

func TestIntegration_PathB_UnknownSnapshot(t *testing.T) {
	ctx := context.Background()
	f := newFixture(ctx, t)

	if _, err := f.backup.RunBackup(ctx, spaceID); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}
	if _, err := f.restore.RunRestore(ctx, spaceID, "no-such-snapshot"); !errors.Is(err, restore.ErrSnapshotNotFound) {
		t.Fatalf("err = %v, want ErrSnapshotNotFound", err)
	}
}

func totalBytes(ctx context.Context, t *testing.T, f *fixture) int64 {
	t.Helper()
	client := f.garage.S3Client(ctx, t)

	var total int64
	pager := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(f.garage.Bucket)})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			t.Fatalf("list objects: %v", err)
		}
		for _, o := range page.Contents {
			total += aws.ToInt64(o.Size)
		}
	}
	return total
}
