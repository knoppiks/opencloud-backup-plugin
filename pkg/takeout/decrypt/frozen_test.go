package decrypt

// Frozen Take-Outs: the proof that decrypt still opens what an earlier
// release wrote (compatibility-policy.md §2, review-2026-10.md G6).
//
// Every other test here builds its repository with the kopia and the code of
// the same commit, so a kopia upgrade that can no longer read an old
// repository, or a format change that forgets its old version, passes them
// all. These fixtures were written once, by the takeout of their day, and are
// never regenerated:
//
//	../testdata/<name>/       the Take-Out directory, byte for byte
//	../testdata/<name>.json   what it holds and the throwaway Recovery Key
//	                          generated for it (allowlisted in .gitleaks.toml)
//
// Every new Take-Out manifest or key-envelope version adds a fixture of its
// own (TestFrozenTakeOutsCoverTheCurrentFormats fails until it does); none is
// ever deleted (frozenTakeOuts). To add one, pick a new name and run:
//
//	go test ./pkg/takeout/decrypt -run TestFreezeTakeOut -freeze <name>
//
// then add the name to frozenTakeOuts. The generator refuses to overwrite.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/objstore"
	"opencloud-backup-plugin/pkg/snapshot"
	"opencloud-backup-plugin/pkg/takeout"
)

// frozenTakeOuts lists every fixture ever frozen. Append only: removing a
// name is removing the proof that its Take-Outs still open.
var frozenTakeOuts = []string{
	"takeout-v1",
}

var freezeName = flag.String("freeze", "", "write a new frozen Take-Out under ../testdata/<name> (never overwrites)")

const frozenDir = "../testdata"

// frozenMeta is the sidecar of a frozen Take-Out.
type frozenMeta struct {
	Comment     string `json:"_comment"`
	Written     string `json:"written"`
	WrittenBy   writer `json:"written_by"`
	RecoveryKey string `json:"recovery_key"`
	SpaceID     string `json:"space_id"`
	// Snapshots are newest first, the order ListSnapshots returns.
	Snapshots []frozenSnapshot `json:"snapshots"`
}

type writer struct {
	Go              string `json:"go"`
	Kopia           string `json:"kopia"`
	ManifestVersion int    `json:"manifest_version"`
	EnvelopeVersion int    `json:"envelope_version"`
}

type frozenSnapshot struct {
	ID    string                `json:"id"`
	Files map[string]frozenFile `json:"files"`
}

type frozenFile struct {
	Size    int64     `json:"size"`
	SHA256  string    `json:"sha256"`
	ModTime time.Time `json:"mod_time"`
}

func TestFrozenTakeOutsAreAllListed(t *testing.T) {
	sidecars, err := filepath.Glob(filepath.Join(frozenDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, s := range sidecars {
		found = append(found, strings.TrimSuffix(filepath.Base(s), ".json"))
	}
	for _, name := range frozenTakeOuts {
		if !slices.Contains(found, name) {
			t.Errorf("frozen Take-Out %q is gone; frozen fixtures are never deleted", name)
		}
	}
	for _, name := range found {
		if !slices.Contains(frozenTakeOuts, name) {
			t.Errorf("%s.json is not in frozenTakeOuts; list it so its deletion is noticed", name)
		}
	}
}

// TestFrozenTakeOuts restores every snapshot of every frozen Take-Out with
// today's decrypt and compares it with what was frozen.
func TestFrozenTakeOuts(t *testing.T) {
	for _, name := range frozenTakeOuts {
		t.Run(name, func(t *testing.T) {
			meta := readFrozenMeta(t, name)
			dir := copyFrozen(t, name)

			if err := takeout.Verify(context.Background(), dir); err != nil {
				t.Fatalf("Verify: %v", err)
			}
			rk, err := keys.DecodeRecoveryKey(meta.RecoveryKey)
			if err != nil {
				t.Fatalf("the frozen Recovery Key no longer decodes: %v", err)
			}

			snaps, err := ListSnapshots(context.Background(), Options{Dir: dir, RecoveryKey: rk, WorkDir: t.TempDir()})
			if err != nil {
				t.Fatalf("ListSnapshots: %v", err)
			}
			var ids []string
			for _, s := range snaps {
				ids = append(ids, s.ID)
			}
			var want []string
			for _, s := range meta.Snapshots {
				want = append(want, s.ID)
			}
			if !slices.Equal(ids, want) {
				t.Fatalf("snapshots = %v, want %v", ids, want)
			}

			for _, snap := range meta.Snapshots {
				out := filepath.Join(t.TempDir(), "restored")
				res, err := Decrypt(context.Background(), Options{
					Dir: dir, RecoveryKey: rk, OutDir: out, SnapshotID: snap.ID, WorkDir: t.TempDir(),
				})
				if err != nil {
					t.Fatalf("Decrypt %s: %v", snap.ID, err)
				}
				if res.SpaceID != meta.SpaceID {
					t.Fatalf("space = %q, want %q", res.SpaceID, meta.SpaceID)
				}
				if got := describeTree(t, out); !equalFiles(got, snap.Files) {
					t.Fatalf("snapshot %s restored\n%v\nwant\n%v", snap.ID, got, snap.Files)
				}
			}
		})
	}
}

// TestFrozenTakeOutsCoverTheCurrentFormats fails when takeout writes a
// manifest or an envelope version no frozen fixture has: the moment to freeze
// a new one is the change that introduces the version.
func TestFrozenTakeOutsCoverTheCurrentFormats(t *testing.T) {
	type formats struct{ manifest, envelope int }
	var covered []formats
	for _, name := range frozenTakeOuts {
		dir := filepath.Join(frozenDir, name)
		m, err := takeout.ReadManifest(dir)
		if err != nil {
			t.Fatalf("%s: ReadManifest: %v", name, err)
		}
		blob, err := os.ReadFile(m.EnvelopePath(dir))
		if err != nil {
			t.Fatalf("%s: read envelope: %v", name, err)
		}
		info, err := keys.Inspect(blob)
		if err != nil {
			t.Fatalf("%s: Inspect: %v", name, err)
		}
		covered = append(covered, formats{m.Version, info.Version})
	}
	current := formats{takeout.ManifestVersion, keys.EnvelopeVersion}
	if !slices.Contains(covered, current) {
		t.Fatalf("no frozen Take-Out has manifest version %d with envelope version %d; freeze one (see this file's comment)",
			current.manifest, current.envelope)
	}
}

// TestFreezeTakeOut writes a new frozen Take-Out with today's code. It runs
// only when asked to (-freeze <name>).
func TestFreezeTakeOut(t *testing.T) {
	if *freezeName == "" {
		t.Skip("run with -freeze <name> to write a new frozen Take-Out")
	}
	name := *freezeName
	target := filepath.Join(frozenDir, name)
	sidecar := target + ".json"
	for _, p := range []string{target, sidecar} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s exists; frozen Take-Outs are never rewritten, pick a new name", p)
		}
	}

	ctx := context.Background()
	f := newFixture(t)
	// A second snapshot, so the fixture also pins listing and choosing one.
	info, err := f.engine.Snapshot(ctx, f.repo, newMemSource(map[string]string{
		"readme.txt":     "second version",
		"docs/notes.txt": f.files["docs/notes.txt"],
		"empty.txt":      "",
	}))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	f.snapIDs = append(f.snapIDs, info.ID)

	if _, err := takeout.Extract(ctx, takeout.ExtractOptions{
		Repos:    snapshot.FilesystemOpener{Root: f.bucket},
		Objects:  objstore.DirStore{Root: f.bucket},
		Location: snapshot.Location{Prefix: testPrefix},
		SpaceID:  testSpaceID,
		OutDir:   target,
	}); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	meta := frozenMeta{
		Comment: "Frozen Take-Out, written once by `go test ./pkg/takeout/decrypt -run TestFreezeTakeOut -freeze " + name +
			"`. Never regenerate or edit it. The Recovery Key is a throwaway generated for this fixture and opens nothing else.",
		Written: time.Now().UTC().Format(time.DateOnly),
		WrittenBy: writer{
			Go:              runtime.Version(),
			Kopia:           moduleVersion(t, "github.com/kopia/kopia"),
			ManifestVersion: takeout.ManifestVersion,
			EnvelopeVersion: keys.EnvelopeVersion,
		},
		RecoveryKey: keys.EncodeRecoveryKey(f.rk),
		SpaceID:     testSpaceID,
	}
	// Restore each snapshot with the code that wrote it, and record what came
	// out: that is what every later decrypt has to reproduce.
	for _, id := range slices.Backward(f.snapIDs) {
		out := filepath.Join(t.TempDir(), "restored")
		if _, err := Decrypt(ctx, Options{
			Dir: copyDir(t, target), RecoveryKey: f.rk, OutDir: out, SnapshotID: string(id), WorkDir: t.TempDir(),
		}); err != nil {
			t.Fatalf("Decrypt %s: %v", id, err)
		}
		meta.Snapshots = append(meta.Snapshots, frozenSnapshot{ID: string(id), Files: describeTree(t, out)})
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecar, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("froze %s; add %q to frozenTakeOuts", target, name)
}

func readFrozenMeta(t *testing.T, name string) frozenMeta {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(frozenDir, name+".json"))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var meta frozenMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parse sidecar: %v", err)
	}
	if len(meta.Snapshots) == 0 || meta.RecoveryKey == "" {
		t.Fatalf("sidecar of %s is incomplete", name)
	}
	return meta
}

// copyFrozen copies a frozen Take-Out to a temporary directory, so no test run
// can change the committed bytes, whatever kopia does on open.
func copyFrozen(t *testing.T, name string) string {
	t.Helper()
	return copyDir(t, filepath.Join(frozenDir, name))
}

func copyDir(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
	return dst
}

// describeTree records every regular file under root by size, digest and
// modification time.
func describeTree(t *testing.T, root string) map[string]frozenFile {
	t.Helper()
	files := map[string]frozenFile{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		files[filepath.ToSlash(rel)] = frozenFile{
			Size:    int64(len(data)),
			SHA256:  hex.EncodeToString(sum[:]),
			ModTime: fi.ModTime().UTC().Truncate(time.Second),
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return files
}

func equalFiles(a, b map[string]frozenFile) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || va.Size != vb.Size || va.SHA256 != vb.SHA256 || !va.ModTime.Equal(vb.ModTime) {
			return false
		}
	}
	return true
}

// moduleVersion reports the version of a dependency as go.mod requires it.
// Test binaries carry no dependency list in their build info.
func moduleVersion(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile("../../../go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == path {
			return fields[1]
		}
	}
	t.Fatalf("go.mod does not require %s", path)
	return ""
}
