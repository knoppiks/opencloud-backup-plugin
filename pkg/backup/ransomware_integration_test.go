//go:build integration

package backup

// The claim this project exists for, as a test (decisions.md #9 Tier 1, threat
// model "Primary threat").
//
// The threat model's chain is: a family member's machine is encrypted by
// ransomware, the OpenCloud client syncs the encrypted files up, and the next
// scheduled run backs the garbage up. The defence is not that the bad snapshot
// is prevented — it is not, and cannot be, because the server cannot tell
// encrypted files from a large import. The defence is that the bad snapshot is
// *one more snapshot*, and yesterday's is still there and still restorable.
//
// Everything else in the project assumes this. Until now nothing checked it.
//
// Run: go test -tags integration ./pkg/backup/...

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/spacecfg"
)

// theGoodFiles is what the Space holds before the attack, and what a restore of
// the pre-attack snapshot must produce afterwards.
var theGoodFiles = map[string]string{
	"readme.txt":     "top-secret plaintext marker ALPHA",
	"docs/notes.txt": "nested marker BRAVO with more text",
	"фото/café.txt":  "unicode path content",
}

func TestIntegration_RansomwareDoesNotEvictGoodHistory(t *testing.T) {
	ctx := context.Background()
	p := newGaragePipeline(ctx, t)

	good, err := p.runner.RunBackup(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("the good backup: %v", err)
	}

	encryptEverything(t, p)

	bad, err := p.runner.RunBackup(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("the backup of the encrypted Space: %v", err)
	}
	if bad.SnapshotID == good.SnapshotID {
		t.Fatal("the second run did not produce a new snapshot")
	}

	// Retention runs, with the Space's own window — the thing that would evict
	// history if anything were going to. Nothing here is a week old, so nothing
	// may go.
	prune, err := p.runner.RunPrune(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("RunPrune: %v", err)
	}
	if prune.Deleted != 0 {
		t.Fatalf("retention deleted %d snapshots inside the window", prune.Deleted)
	}

	list, err := p.engine.List(ctx, p.repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("the repository holds %d snapshots, want the good one and the bad one", len(list))
	}

	// The whole point: yesterday's snapshot restores to yesterday's files.
	out := t.TempDir()
	if err := p.engine.RestoreAll(ctx, p.repo, good.SnapshotID, out); err != nil {
		t.Fatalf("restoring the pre-attack snapshot: %v", err)
	}
	for path, want := range theGoodFiles {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read restored %s: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("restored %s = %q, want the pre-attack content %q", path, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "READ_ME_TO_DECRYPT.txt")); err == nil {
		t.Fatal("the ransom note is present in the pre-attack snapshot")
	}

	// And the bad snapshot really is bad, so the test above is not passing
	// because the attack never happened.
	attacked := t.TempDir()
	if err := p.engine.RestoreAll(ctx, p.repo, bad.SnapshotID, attacked); err != nil {
		t.Fatalf("restoring the post-attack snapshot: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(attacked, "readme.txt"))
	if err != nil {
		t.Fatalf("read attacked readme: %v", err)
	}
	if string(got) == theGoodFiles["readme.txt"] {
		t.Fatal("the simulated attack did not change the Space")
	}
	if _, err := os.Stat(filepath.Join(attacked, "READ_ME_TO_DECRYPT.txt")); err != nil {
		t.Fatalf("the ransom note is missing from the post-attack snapshot: %v", err)
	}
}

// TestIntegration_RansomwareCannotShortenRetention is the other half of the
// same threat. Snapshot depth is the defence, and the retention window is a
// number an attacker holding a member's browser session could otherwise set to
// nothing — after which one prune would destroy the history the test above
// depends on.
func TestIntegration_RansomwareCannotShortenRetention(t *testing.T) {
	ctx := context.Background()
	p := newGaragePipeline(ctx, t)

	good, err := p.runner.RunBackup(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("the good backup: %v", err)
	}
	encryptEverything(t, p)
	if _, err := p.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("the backup of the encrypted Space: %v", err)
	}

	// The attacker writes the shortest window the stored record can hold. The
	// API would refuse it, which is why this goes in behind the API: the record
	// is what prune reads, and the floor has to hold there too.
	//
	// The value matters. A window of a day would leave both snapshots in place
	// whether or not the floor existed, and the test would pass while checking
	// nothing. One nanosecond is older than the good snapshot the instant it is
	// written, so without the floor this prune deletes it and leaves the
	// household with the ransomware snapshot as its only copy.
	cfg, err := p.runner.deps.Configs.Get(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("Get config: %v", err)
	}
	cfg.RetentionWindow = time.Nanosecond
	if _, err := p.runner.deps.Configs.Put(ctx, cfg); err != nil {
		t.Fatalf("Put config: %v", err)
	}
	if got := cfg.EffectiveRetentionWindow(); got != spacecfg.MinRetentionWindow {
		t.Fatalf("effective window = %s, want the floor %s", got, spacecfg.MinRetentionWindow)
	}

	prune, err := p.runner.RunPrune(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("RunPrune: %v", err)
	}
	if prune.Deleted != 0 {
		t.Fatalf("a sub-floor window expired %d snapshots", prune.Deleted)
	}
	if prune.Kept != 2 {
		t.Fatalf("prune kept %d snapshots, want both", prune.Kept)
	}

	out := t.TempDir()
	if err := p.engine.RestoreAll(ctx, p.repo, good.SnapshotID, out); err != nil {
		t.Fatalf("the pre-attack snapshot did not survive a shortened window: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, "readme.txt"))
	if err != nil {
		t.Fatalf("read restored readme: %v", err)
	}
	if string(got) != theGoodFiles["readme.txt"] {
		t.Fatalf("restored readme = %q", got)
	}
}

// encryptEverything does to the Space what ransomware does to a laptop: every
// file replaced with bytes nobody can read, and a ransom note dropped beside
// them. The mtimes move forward, because the files really were rewritten — a
// simulation that left them alone would be quietly testing kopia's cache
// instead of the attack.
func encryptEverything(t *testing.T, p *garagePipeline) {
	t.Helper()

	attacked := testMTime.Add(24 * time.Hour)
	for path := range theGoodFiles {
		garbage := make([]byte, 4096)
		if _, err := rand.Read(garbage); err != nil {
			t.Fatalf("generate encrypted content: %v", err)
		}
		p.reader.put(path, garbage, attacked)
	}

	// The large file too: partial damage is not the scenario.
	damaged := make([]byte, len(p.bigFile))
	if _, err := rand.Read(damaged); err != nil {
		t.Fatalf("generate encrypted content: %v", err)
	}
	if bytes.Equal(damaged, p.bigFile) {
		t.Fatal("the universe is broken")
	}
	p.reader.put("assets/große-datei.bin", damaged, attacked)

	p.reader.put("READ_ME_TO_DECRYPT.txt",
		[]byte("your files have been encrypted, send money"), attacked)
}
