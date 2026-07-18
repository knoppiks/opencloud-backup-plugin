//go:build integration

// Package main (spikes/kopia) is throwaway Phase-0 exploration code (Spike 2).
// It validates that kopia's importable Go API — with no kopia CLI — can drive
// the full lifecycle we depend on against an ephemeral Garage bucket:
//
//  1. create a kopia repo on the Garage bucket (repo password = DK stand-in)
//  2. snapshot a local directory (nested dirs, non-ASCII names, a large file)
//  3. confirm the bucket holds only obfuscated/encrypted blobs (no plaintext)
//  4. second snapshot after a small change -> dedup (marginal upload)
//  5. full restore into an empty dir -> byte-identical + mtime preserved
//  6. single-file restore (pins the API for the backlog feature)
//  7. time-based prune (keep-within) + maintenance GC -> old data removed
//
// Learnings land in .agents/plan/phase-0-spikes.md and decisions.md; this code
// is not promoted. Run: go test -tags integration ./spikes/kopia/...
package main

import (
	"bytes"
	"context"
	"io"
	"math"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/kopia/kopia/fs/localfs"
	"github.com/kopia/kopia/repo"
	"github.com/kopia/kopia/repo/blob"
	kopias3 "github.com/kopia/kopia/repo/blob/s3"
	"github.com/kopia/kopia/repo/maintenance"
	"github.com/kopia/kopia/repo/manifest"
	"github.com/kopia/kopia/snapshot"
	"github.com/kopia/kopia/snapshot/policy"
	"github.com/kopia/kopia/snapshot/restore"
	"github.com/kopia/kopia/snapshot/snapshotfs"
	"github.com/kopia/kopia/snapshot/upload"

	"opencloud-backup-plugin/internal/testutil"
)

// dataKey stands in for the per-Space Data Key (DK). In production this is a
// 256-bit random key; here any strong password works as the kopia repo secret.
const dataKey = "spike-data-key-0123456789abcdef-not-a-real-DK"

func TestKopiaLifecycle(t *testing.T) {
	ctx := context.Background()
	g := testutil.StartGarage(ctx, t)

	// kopia keeps a small connection config + cache on local disk; the repo
	// data itself lives in the Garage bucket.
	work := t.TempDir()
	configFile := filepath.Join(work, "repository.config")

	st := newGarageStorage(ctx, t, g)

	// --- Step 1: create repo on the Garage bucket -------------------------
	if err := repo.Initialize(ctx, st, &repo.NewRepositoryOptions{}, dataKey); err != nil {
		t.Fatalf("repo.Initialize: %v", err)
	}
	if err := repo.Connect(ctx, configFile, st, dataKey, &repo.ConnectOptions{}); err != nil {
		t.Fatalf("repo.Connect: %v", err)
	}

	src := makeSourceTree(t, work)
	sourceInfo := snapshot.SourceInfo{Host: "spike-host", UserName: "spike-user", Path: src}

	// --- Step 2: first snapshot ------------------------------------------
	sizeBeforeSnap1 := totalBlobBytes(ctx, t, st)
	man1 := runSnapshot(ctx, t, configFile, sourceInfo, src)
	sizeAfterSnap1 := totalBlobBytes(ctx, t, st)
	uploadedSnap1 := sizeAfterSnap1 - sizeBeforeSnap1
	t.Logf("snapshot 1: id=%s files=%d totalBytes=%d uploadedBytes=%d",
		man1.ID, man1.Stats.TotalFileCount, man1.Stats.TotalFileSize, uploadedSnap1)

	// --- Step 3: bucket holds no plaintext -------------------------------
	assertNoPlaintextInBucket(ctx, t, g, [][]byte{
		[]byte("top-secret plaintext marker ALPHA"),
		[]byte("nested marker BRAVO with more text"),
		[]byte("große-datei"), // non-ASCII filename fragment
	})

	// --- Step 4: change one small file, snapshot again, expect dedup -----
	appendToFile(t, filepath.Join(src, "docs", "notes.txt"), []byte("\none more line\n"))
	sizeBeforeSnap2 := totalBlobBytes(ctx, t, st)
	man2 := runSnapshot(ctx, t, configFile, sourceInfo, src)
	sizeAfterSnap2 := totalBlobBytes(ctx, t, st)
	uploadedSnap2 := sizeAfterSnap2 - sizeBeforeSnap2
	t.Logf("snapshot 2: id=%s uploadedBytes=%d (snapshot1 uploadedBytes=%d)",
		man2.ID, uploadedSnap2, uploadedSnap1)

	// Dedup: the second run must upload far fewer bytes than the first, because
	// only the changed file (plus new metadata) is written. The large unchanged
	// 24 MiB binary must not be re-uploaded.
	if uploadedSnap2 >= uploadedSnap1 {
		t.Fatalf("expected dedup: snapshot2 uploaded %d bytes should be << snapshot1 %d",
			uploadedSnap2, uploadedSnap1)
	}
	if uploadedSnap2 > 1<<20 {
		t.Fatalf("expected marginal upload after 1-line change, got %d bytes", uploadedSnap2)
	}

	// --- Step 5: full restore, byte-identical + mtime --------------------
	restoreDir := filepath.Join(work, "restore-full")
	fullRestore(ctx, t, configFile, man2, restoreDir)
	assertTreesEqual(t, src, restoreDir)

	// --- Step 6: single-file restore -------------------------------------
	singleOut := filepath.Join(work, "restore-single.bin")
	singleFileRestore(ctx, t, configFile, man2, []string{"assets", "große-datei.bin"}, singleOut)
	assertFilesEqual(t, filepath.Join(src, "assets", "große-datei.bin"), singleOut)

	// --- Step 7: time-based prune + GC -----------------------------------
	// Keep only snapshots within a window that excludes snapshot 1 but keeps
	// snapshot 2. We implement keep-within ourselves (kopia's RetentionPolicy
	// is count-based only) by deleting manifests older than the cutoff, then
	// running full maintenance GC to drop now-unreferenced content.
	beforeGC := countBlobs(ctx, t, st)
	timeBasedPrune(ctx, t, configFile, sourceInfo, man1.ID)
	afterGC := countBlobs(ctx, t, st)
	t.Logf("prune: blob count before GC=%d after GC=%d", beforeGC, afterGC)

	// snapshot 2 must still fully restore after pruning snapshot 1.
	restoreDir2 := filepath.Join(work, "restore-after-prune")
	fullRestore(ctx, t, configFile, reloadManifest(ctx, t, configFile, man2.ID), restoreDir2)
	assertTreesEqual(t, src, restoreDir2)

	// snapshot 1 must be gone from the manifest list.
	if manifestExists(ctx, t, configFile, sourceInfo, man1.ID) {
		t.Fatalf("snapshot 1 manifest %s still present after prune", man1.ID)
	}
}

// newGarageStorage builds a kopia S3 blob.Storage pointing at the Garage
// fixture. Garage endpoint has no scheme for kopia; TLS disabled for the test.
func newGarageStorage(ctx context.Context, t *testing.T, g *testutil.Garage) blob.Storage {
	t.Helper()
	endpoint := stripScheme(g.Endpoint)
	st, err := kopias3.New(ctx, &kopias3.Options{
		BucketName:      g.Bucket,
		Endpoint:        endpoint,
		DoNotUseTLS:     true,
		AccessKeyID:     g.AccessKeyID,
		SecretAccessKey: g.SecretAccessKey,
		Region:          g.Region,
		Prefix:          "spike/",
	}, true)
	if err != nil {
		t.Fatalf("s3.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	return st
}

func runSnapshot(ctx context.Context, t *testing.T, configFile string, si snapshot.SourceInfo, dir string) *snapshot.Manifest {
	t.Helper()
	rep := openRepo(ctx, t, configFile)
	defer rep.Close(ctx)

	var man *snapshot.Manifest
	err := repo.WriteSession(ctx, rep, repo.WriteSessionOptions{Purpose: "spike-snapshot"},
		func(ctx context.Context, w repo.RepositoryWriter) error {
			sourceEntry, err := localfs.NewEntry(dir)
			if err != nil {
				return err
			}
			policyTree, err := policy.TreeForSource(ctx, w, si)
			if err != nil {
				return err
			}
			u := upload.NewUploader(w)
			prev, err := snapshot.ListSnapshots(ctx, w, si)
			if err != nil {
				return err
			}
			m, err := u.Upload(ctx, sourceEntry, policyTree, si, prev...)
			if err != nil {
				return err
			}
			if _, err := snapshot.SaveSnapshot(ctx, w, m); err != nil {
				return err
			}
			man = m
			return nil
		})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return man
}

func fullRestore(ctx context.Context, t *testing.T, configFile string, man *snapshot.Manifest, target string) {
	t.Helper()
	rep := openRepo(ctx, t, configFile)
	defer rep.Close(ctx)

	root, err := snapshotfs.SnapshotRoot(rep, man)
	if err != nil {
		t.Fatalf("SnapshotRoot: %v", err)
	}
	out := &restore.FilesystemOutput{
		TargetPath:           target,
		OverwriteDirectories: true,
		OverwriteFiles:       true,
	}
	// FilesystemOutput must be initialised before use (sets up its stream
	// copier); restore.Entry does not do this for us.
	if err := out.Init(ctx); err != nil {
		t.Fatalf("output.Init: %v", err)
	}
	// RestoreDirEntryAtDepth defaults to 0, which produces shallow .kopia-entry
	// placeholders. Set it to max so the entire tree is materialised.
	if _, err := restore.Entry(ctx, rep, out, root, restore.Options{
		RestoreDirEntryAtDepth: math.MaxInt32,
	}); err != nil {
		t.Fatalf("restore.Entry: %v", err)
	}
}

func singleFileRestore(ctx context.Context, t *testing.T, configFile string, man *snapshot.Manifest, pathElements []string, targetFile string) {
	t.Helper()
	rep := openRepo(ctx, t, configFile)
	defer rep.Close(ctx)

	root, err := snapshotfs.SnapshotRoot(rep, man)
	if err != nil {
		t.Fatalf("SnapshotRoot: %v", err)
	}
	entry, err := snapshotfs.GetNestedEntry(ctx, root, pathElements)
	if err != nil {
		t.Fatalf("GetNestedEntry %v: %v", pathElements, err)
	}
	out := &restore.FilesystemOutput{
		TargetPath:     targetFile,
		OverwriteFiles: true,
	}
	if err := out.Init(ctx); err != nil {
		t.Fatalf("output.Init: %v", err)
	}
	if _, err := restore.Entry(ctx, rep, out, entry, restore.Options{
		RestoreDirEntryAtDepth: math.MaxInt32,
	}); err != nil {
		t.Fatalf("single-file restore.Entry: %v", err)
	}
}

// timeBasedPrune implements keep-within retention ourselves: delete the target
// (old) snapshot manifest, then run full maintenance with no safety delay to
// GC now-unreferenced content blobs immediately.
func timeBasedPrune(ctx context.Context, t *testing.T, configFile string, si snapshot.SourceInfo, oldID manifest.ID) {
	t.Helper()
	rep := openRepo(ctx, t, configFile)
	defer rep.Close(ctx)

	// Delete the manifest of the old snapshot.
	err := repo.WriteSession(ctx, rep, repo.WriteSessionOptions{Purpose: "spike-prune"},
		func(ctx context.Context, w repo.RepositoryWriter) error {
			mans, err := snapshot.ListSnapshots(ctx, w, si)
			if err != nil {
				return err
			}
			for _, m := range mans {
				if m.ID == oldID {
					if err := w.DeleteManifest(ctx, m.ID); err != nil {
						return err
					}
				}
			}
			return nil
		})
	if err != nil {
		t.Fatalf("delete old manifest: %v", err)
	}

	// GC unreferenced content via full maintenance. Requires a direct repo.
	dr, ok := rep.(repo.DirectRepository)
	if !ok {
		t.Fatalf("repo is not a DirectRepository; cannot run maintenance")
	}
	err = repo.DirectWriteSession(ctx, dr, repo.WriteSessionOptions{Purpose: "spike-gc"},
		func(ctx context.Context, dw repo.DirectRepositoryWriter) error {
			// Ensure maintenance is owned by this user so RunExclusive proceeds.
			p, err := maintenance.GetParams(ctx, dw)
			if err != nil {
				return err
			}
			if p.Owner == "" {
				def := maintenance.DefaultParams()
				def.Owner = dw.ClientOptions().UsernameAtHost()
				if err := maintenance.SetParams(ctx, dw, &def); err != nil {
					return err
				}
			}
			return maintenance.RunExclusive(ctx, dw, maintenance.ModeFull, true,
				func(ctx context.Context, rp maintenance.RunParameters) error {
					return maintenance.Run(ctx, rp, maintenance.SafetyNone)
				})
		})
	if err != nil {
		t.Fatalf("maintenance GC: %v", err)
	}
}

func openRepo(ctx context.Context, t *testing.T, configFile string) repo.Repository {
	t.Helper()
	rep, err := repo.Open(ctx, configFile, dataKey, &repo.Options{})
	if err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	return rep
}

func reloadManifest(ctx context.Context, t *testing.T, configFile string, id manifest.ID) *snapshot.Manifest {
	t.Helper()
	rep := openRepo(ctx, t, configFile)
	defer rep.Close(ctx)
	sources, err := snapshot.ListSources(ctx, rep)
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	for _, s := range sources {
		mans, err := snapshot.ListSnapshots(ctx, rep, s)
		if err != nil {
			t.Fatalf("ListSnapshots: %v", err)
		}
		for _, m := range mans {
			if m.ID == id {
				return m
			}
		}
	}
	t.Fatalf("manifest %s not found on reload", id)
	return nil
}

func manifestExists(ctx context.Context, t *testing.T, configFile string, si snapshot.SourceInfo, id manifest.ID) bool {
	t.Helper()
	rep := openRepo(ctx, t, configFile)
	defer rep.Close(ctx)
	mans, err := snapshot.ListSnapshots(ctx, rep, si)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	for _, m := range mans {
		if m.ID == id {
			return true
		}
	}
	return false
}

func countBlobs(ctx context.Context, t *testing.T, st blob.Storage) int {
	t.Helper()
	n := 0
	err := st.ListBlobs(ctx, "", func(bm blob.Metadata) error {
		n++
		return nil
	})
	if err != nil {
		t.Fatalf("ListBlobs: %v", err)
	}
	return n
}

func totalBlobBytes(ctx context.Context, t *testing.T, st blob.Storage) int64 {
	t.Helper()
	var total int64
	err := st.ListBlobs(ctx, "", func(bm blob.Metadata) error {
		total += bm.Length
		return nil
	})
	if err != nil {
		t.Fatalf("ListBlobs: %v", err)
	}
	return total
}

// assertNoPlaintextInBucket lists every object directly from Garage (via the S3
// client, bypassing kopia) and scans keys + bodies for known plaintext markers
// and source filenames. This proves kopia encrypts content and obfuscates paths
// at rest.
func assertNoPlaintextInBucket(ctx context.Context, t *testing.T, g *testutil.Garage, markers [][]byte) {
	t.Helper()
	client := g.S3Client(ctx, t)

	var keys []string
	p := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(g.Bucket)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			t.Fatalf("list objects: %v", err)
		}
		for _, o := range page.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
	}
	if len(keys) == 0 {
		t.Fatal("no objects found in bucket after snapshot")
	}

	forbiddenInKeys := []string{"große-datei", "notes.txt", "readme.txt", "café", "фото"}
	for _, key := range keys {
		for _, f := range forbiddenInKeys {
			if bytes.Contains([]byte(key), []byte(f)) {
				t.Fatalf("source filename %q leaked into object key %q", f, key)
			}
		}
		out, err := client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(g.Bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			t.Fatalf("get object %s: %v", key, err)
		}
		body, err := io.ReadAll(out.Body)
		_ = out.Body.Close()
		if err != nil {
			t.Fatalf("read object %s: %v", key, err)
		}
		for _, m := range markers {
			if bytes.Contains(body, m) {
				t.Fatalf("plaintext marker %q leaked into object %s", m, key)
			}
		}
	}
}
