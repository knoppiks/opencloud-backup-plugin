package targets

import (
	"context"
	"testing"

	"opencloud-backup-plugin/pkg/keys"
)

func testSealer(t *testing.T) CredSealer {
	t.Helper()
	twKey, err := keys.GenerateSRWKey()
	if err != nil {
		t.Fatalf("GenerateSRWKey: %v", err)
	}
	sealer, err := NewCredSealer(twKey)
	if err != nil {
		t.Fatalf("NewCredSealer: %v", err)
	}
	return sealer
}

func validBootstrap() BootstrapConfig {
	return BootstrapConfig{
		Enable:       true,
		Name:         "Buddy-S3",
		Endpoint:     "http://garage:3900",
		Region:       "garage",
		Bucket:       "opencloud-backup",
		UsePathStyle: true,
		DisableTLS:   true,
		Creds: PlainCreds{
			AccessKeyID:     "GK-bootstrap",
			SecretAccessKey: "bootstrap-secret-0000000000000000000000",
		},
	}
}

func TestBootstrap_SeedsTargetAndGrantsAllUsers(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	sealer := testSealer(t)

	seeded, err := Bootstrap(ctx, store, sealer, validBootstrap())
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if !seeded {
		t.Fatal("Bootstrap must report that it seeded")
	}

	target, err := store.GetTarget(ctx, DefaultBootstrapID)
	if err != nil {
		t.Fatalf("GetTarget: %v", err)
	}
	if target.Bucket != "opencloud-backup" || !target.DisableTLS || !target.UsePathStyle {
		t.Fatalf("target = %+v", target.Public())
	}

	// Credentials are stored only as sealed ciphertext.
	if len(target.WrappedCreds) == 0 {
		t.Fatal("credentials were not sealed")
	}
	for _, secret := range []string{"GK-bootstrap", "bootstrap-secret-0000000000000000000000"} {
		if containsBytes(target.WrappedCreds, secret) {
			t.Fatalf("plaintext credential %q found in the stored blob", secret)
		}
	}
	opened, err := sealer.Open(target.WrappedCreds)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened.Backup != validBootstrap().Creds {
		t.Fatal("sealed credentials did not round-trip")
	}
	// Seeding one key is the single-credential deployment: both roles use it.
	if !opened.Maintenance.Empty() {
		t.Fatalf("no maintenance credential was configured, got %+v", opened.Maintenance)
	}

	// A single seeded target is visible to any user, keeping setup one click.
	views, err := store.VisibleTargets(ctx, "someone", nil)
	if err != nil {
		t.Fatalf("VisibleTargets: %v", err)
	}
	if len(views) != 1 || views[0].ID != DefaultBootstrapID {
		t.Fatalf("visible targets = %+v", views)
	}
}

func TestBootstrap_DisabledIsNoop(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	cfg := validBootstrap()
	cfg.Enable = false
	seeded, err := Bootstrap(ctx, store, testSealer(t), cfg)
	if err != nil || seeded {
		t.Fatalf("disabled bootstrap seeded=%v err=%v", seeded, err)
	}
	if list, _ := store.ListTargets(ctx); len(list) != 0 {
		t.Fatalf("store not empty: %+v", list)
	}
}

// Seeding must never overwrite what an admin configured.
func TestBootstrap_SkipsWhenTargetsExist(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	if _, err := store.CreateTarget(ctx, Target{ID: "admin-made", Name: "Admin"}); err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}

	seeded, err := Bootstrap(ctx, store, testSealer(t), validBootstrap())
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if seeded {
		t.Fatal("bootstrap must not seed when targets already exist")
	}
	if _, err := store.GetTarget(ctx, DefaultBootstrapID); err == nil {
		t.Fatal("bootstrap created a target despite existing ones")
	}
}

func TestBootstrap_Validation(t *testing.T) {
	ctx := context.Background()
	sealer := testSealer(t)

	cases := map[string]func(*BootstrapConfig){
		"missing endpoint":   func(c *BootstrapConfig) { c.Endpoint = "" },
		"missing bucket":     func(c *BootstrapConfig) { c.Bucket = "" },
		"missing access key": func(c *BootstrapConfig) { c.Creds.AccessKeyID = "" },
		"missing secret key": func(c *BootstrapConfig) { c.Creds.SecretAccessKey = "" },
		"half a maintenance credential": func(c *BootstrapConfig) {
			c.MaintenanceCreds = PlainCreds{AccessKeyID: "GK-maintenance"}
		},
	}
	for name, mutate := range cases {
		cfg := validBootstrap()
		mutate(&cfg)
		if _, err := Bootstrap(ctx, NewMemoryStore(), sealer, cfg); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}

	if _, err := Bootstrap(ctx, nil, sealer, validBootstrap()); err == nil {
		t.Fatal("nil store must be rejected")
	}
	if _, err := Bootstrap(ctx, NewMemoryStore(), nil, validBootstrap()); err == nil {
		t.Fatal("nil sealer must be rejected")
	}
}

// Seeding both roles is how a deployment starts out separated, without an admin
// UI that does not exist yet.
func TestBootstrap_SeedsBothRoles(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	sealer := testSealer(t)

	cfg := validBootstrap()
	cfg.MaintenanceCreds = PlainCreds{
		AccessKeyID:     "GK-maintenance",
		SecretAccessKey: "maintenance-secret-000000000000000000",
	}
	if _, err := Bootstrap(ctx, store, sealer, cfg); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	target, err := store.GetTarget(ctx, DefaultBootstrapID)
	if err != nil {
		t.Fatalf("GetTarget: %v", err)
	}
	for _, secret := range []string{cfg.MaintenanceCreds.AccessKeyID, cfg.MaintenanceCreds.SecretAccessKey} {
		if containsBytes(target.WrappedCreds, secret) {
			t.Fatalf("plaintext maintenance credential %q found in the stored blob", secret)
		}
	}

	opened, err := sealer.Open(target.WrappedCreds)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened.For(RoleBackup) != cfg.Creds {
		t.Fatalf("backup role = %+v", opened.For(RoleBackup))
	}
	if opened.For(RoleMaintenance) != cfg.MaintenanceCreds {
		t.Fatalf("maintenance role = %+v", opened.For(RoleMaintenance))
	}
	if !opened.Separated() {
		t.Fatal("a target seeded with two keys must report as separated")
	}
	// The same fact, answerable without the TW key: the admin API has to show
	// it and may not open the blob to find out (decisions.md #14).
	if !target.MaintenanceConfigured {
		t.Fatal("a separated target must record that it is separated")
	}
}

// A single-key target is not misconfigured, and must not claim separation it
// does not have.
func TestBootstrap_SingleKeyTargetIsNotMarkedSeparated(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	if _, err := Bootstrap(ctx, store, testSealer(t), validBootstrap()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	target, err := store.GetTarget(ctx, DefaultBootstrapID)
	if err != nil {
		t.Fatalf("GetTarget: %v", err)
	}
	if target.MaintenanceConfigured {
		t.Fatal("a target seeded with one key reported a maintenance credential")
	}
}

func TestBootstrap_ErrorsDoNotEchoCredentials(t *testing.T) {
	ctx := context.Background()
	cfg := validBootstrap()
	cfg.Bucket = ""

	_, err := Bootstrap(ctx, NewMemoryStore(), testSealer(t), cfg)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if containsBytes([]byte(err.Error()), cfg.Creds.SecretAccessKey) {
		t.Fatalf("error leaked credentials: %v", err)
	}
}

func containsBytes(haystack []byte, needle string) bool {
	n := []byte(needle)
	for i := 0; i+len(n) <= len(haystack); i++ {
		if string(haystack[i:i+len(n)]) == needle {
			return true
		}
	}
	return false
}
