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
	if opened != validBootstrap().Creds {
		t.Fatal("sealed credentials did not round-trip")
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
