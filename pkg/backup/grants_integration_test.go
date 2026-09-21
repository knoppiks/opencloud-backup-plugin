//go:build integration

package backup

// The least each role needs from a real target, and the separated deployment
// working end to end (decisions.md #9, Tier 2).
//
// internal/testutil pins what Garage's grants permit at the S3 level. These
// tests answer the question that matters to this service: what does *kopia*
// need, driven through the actual runner, with a target whose two roles hold
// two different keys.
//
// Run: go test -tags integration ./pkg/backup/...

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/targets"
)

// roleKeys creates one Garage key per role with the given grants and returns
// them as a credential set. The label makes the key names unique: Garage
// refuses to create two keys with the same name, and a test that asks twice
// wants two different keys.
func roleKeys(
	ctx context.Context,
	t *testing.T,
	g *testutil.Garage,
	label string,
	backupGrants, maintenanceGrants []testutil.Grant,
) targets.CredentialSet {
	t.Helper()

	backup := g.CreateKey(ctx, t, "backup-"+label, backupGrants...)
	maintenance := g.CreateKey(ctx, t, "maintenance-"+label, maintenanceGrants...)
	return targets.CredentialSet{
		Backup:      targets.PlainCreds(backup),
		Maintenance: targets.PlainCreds(maintenance),
	}
}

// readWrite is the grant set both roles actually need on Garage.
var readWrite = []testutil.Grant{testutil.GrantRead, testutil.GrantWrite}

// TestIntegration_BackupNeedsReadAsWellAsWrite is the measurement Phase 7's plan
// asked for and assumed the other way round.
//
// The plan's Tier 2 gives the worker a write-only key so that "a stolen writer
// key cannot exfiltrate existing backups". A kopia repository cannot be opened
// without reading it — the format blob, the indexes, and every deduplication
// decision are reads — so a write-only key does not produce a worker with
// reduced reach. It produces a worker that does not work.
//
// This test is the evidence for that sentence in the operations runbook.
func TestIntegration_BackupNeedsReadAsWellAsWrite(t *testing.T) {
	ctx := context.Background()
	p := newGaragePipeline(ctx, t)

	p.useCredentials(t, roleKeys(ctx, t, p.garage, "write-only",
		[]testutil.Grant{testutil.GrantWrite}, readWrite))

	if _, err := p.runner.RunBackup(ctx, testSpaceID); err == nil {
		t.Fatal("a write-only credential must not be able to run a backup; " +
			"if this now passes, kopia or Garage changed and Tier 2 can promise more")
	}

	// The same run with read added succeeds, so the failure above is about the
	// missing read and not about the fixture.
	p.useCredentials(t, roleKeys(ctx, t, p.garage, "read-write", readWrite, readWrite))
	if _, err := p.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("read+write must be enough for a backup: %v", err)
	}
}

// TestIntegration_PruneNeedsMoreThanOwner pins the other half: Garage's owner
// grant is bucket administration and confers no object access, so the plan's
// "prune-owner" key could not have expired a single snapshot.
func TestIntegration_PruneNeedsMoreThanOwner(t *testing.T) {
	ctx := context.Background()
	p := newGaragePipeline(ctx, t)

	// A backup first, so there is a repository for the prune to open.
	p.useCredentials(t, roleKeys(ctx, t, p.garage, "owner-prune",
		readWrite, []testutil.Grant{testutil.GrantOwner}))
	if _, err := p.runner.RunBackup(ctx, testSpaceID); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}

	if _, err := p.runner.RunPrune(ctx, testSpaceID); err == nil {
		t.Fatal("an owner-only credential must not be able to prune; " +
			"if this now passes, Garage's owner grant changed meaning")
	}
}

// TestIntegration_SeparatedRolesBackUpAndPrune is the deployment this phase
// ships: two distinct keys on one target, a backup run that only ever holds one
// of them, a prune run that only ever holds the other, and a restore afterwards
// that proves the data survived being handled by both.
func TestIntegration_SeparatedRolesBackUpAndPrune(t *testing.T) {
	ctx := context.Background()
	p := newGaragePipeline(ctx, t)

	set := roleKeys(ctx, t, p.garage, "separated", readWrite, readWrite)
	if !set.Separated() {
		t.Fatal("the fixture handed out the same key twice")
	}
	p.useCredentials(t, set)

	res, err := p.runner.RunBackup(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("RunBackup with the backup credential: %v", err)
	}

	prune, err := p.runner.RunPrune(ctx, testSpaceID)
	if err != nil {
		t.Fatalf("RunPrune with the maintenance credential: %v", err)
	}
	// Nothing is old enough to expire: the retention floor is a week.
	if prune.Deleted != 0 || prune.Kept != 1 {
		t.Fatalf("prune result = %+v, want nothing deleted and one snapshot kept", prune)
	}

	out := t.TempDir()
	if err := p.engine.RestoreAll(ctx, p.repo, res.SnapshotID, out); err != nil {
		t.Fatalf("RestoreAll after a separated backup and prune: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, "readme.txt"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(got) != "top-secret plaintext marker ALPHA" {
		t.Fatalf("restored content = %q", got)
	}
}
