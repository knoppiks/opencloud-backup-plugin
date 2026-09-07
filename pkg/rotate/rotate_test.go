package rotate_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/rotate"
	"opencloud-backup-plugin/pkg/targets"
)

func mustKey(t *testing.T) []byte {
	t.Helper()
	key, err := keys.GenerateSRWKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// seedSpaces sets up n spaces with server envelopes under key, returning the
// store and the data keys it wrapped.
func seedSpaces(t *testing.T, key []byte, spaceIDs ...string) (*keys.MemoryStore, map[string][]byte) {
	t.Helper()
	store := keys.NewMemoryStore()
	dataKeys := make(map[string][]byte, len(spaceIDs))
	for _, id := range spaceIDs {
		dk, err := keys.GenerateDK()
		if err != nil {
			t.Fatal(err)
		}
		w, err := keys.WrapWithSRW(dk, key)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutSRW(id, w); err != nil {
			t.Fatal(err)
		}
		dataKeys[id] = dk
	}
	return store, dataKeys
}

// The property rotation exists for: the custody key changes, the Data Key does
// not, and therefore no snapshot needs rewriting.
func TestSRWKeepsEveryDataKey(t *testing.T) {
	oldKey, newKey := mustKey(t), mustKey(t)
	store, dataKeys := seedSpaces(t, oldKey, "space-a", "space-b")

	got, err := rotate.SRW(store, oldKey, newKey)
	if err != nil {
		t.Fatalf("SRW: %v", err)
	}
	if got.Rotated != 2 || got.AlreadyRotated != 0 {
		t.Fatalf("result = %+v, want two rotations", got)
	}

	for id, dk := range dataKeys {
		stored, err := store.GetSRW(id)
		if err != nil {
			t.Fatal(err)
		}
		recovered, err := keys.UnwrapSRW(stored, newKey)
		if err != nil {
			t.Fatalf("%s does not open with the new key: %v", id, err)
		}
		if !bytes.Equal(recovered, dk) {
			t.Fatalf("%s: rotation changed the data key", id)
		}
		if _, err := keys.UnwrapSRW(stored, oldKey); err == nil {
			t.Fatalf("%s still opens with the retired key", id)
		}
	}
}

// A rotation interrupted halfway must be finishable by re-running it: the
// backend has no transactions, so this is the only recovery there is.
func TestSRWIsResumable(t *testing.T) {
	oldKey, newKey := mustKey(t), mustKey(t)
	store, dataKeys := seedSpaces(t, oldKey, "space-a", "space-b")

	// space-a is already on the new key, as if the process died after it.
	dk := dataKeys["space-a"]
	ahead, err := keys.WrapWithSRW(dk, newKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutSRW("space-a", ahead); err != nil {
		t.Fatal(err)
	}

	got, err := rotate.SRW(store, oldKey, newKey)
	if err != nil {
		t.Fatalf("SRW: %v", err)
	}
	if got.Rotated != 1 || got.AlreadyRotated != 1 {
		t.Fatalf("result = %+v, want one rotation and one already done", got)
	}

	// Re-running again is a no-op, not a failure.
	got, err = rotate.SRW(store, oldKey, newKey)
	if err != nil {
		t.Fatalf("second SRW: %v", err)
	}
	if got.Rotated != 0 || got.AlreadyRotated != 2 {
		t.Fatalf("result = %+v, want everything already rotated", got)
	}
}

// The wrong old key must stop the rotation, not quietly rewrite what it can.
func TestSRWRefusesAnEnvelopeThatOpensWithNeitherKey(t *testing.T) {
	realKey, newKey, wrongKey := mustKey(t), mustKey(t), mustKey(t)
	store, _ := seedSpaces(t, realKey, "space-a")

	_, err := rotate.SRW(store, wrongKey, newKey)
	if !errors.Is(err, rotate.ErrKeyMismatch) {
		t.Fatalf("err = %v, want ErrKeyMismatch", err)
	}

	// Nothing was touched: the space still opens with the key it was sealed
	// under.
	stored, err := store.GetSRW("space-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keys.UnwrapSRW(stored, realKey); err != nil {
		t.Fatalf("a refused rotation disturbed the envelope: %v", err)
	}
}

// A half-finished ceremony (RK stored, no SRW yet) must not block every other
// Space's rotation.
func TestSRWSkipsSpacesWithoutAServerEnvelope(t *testing.T) {
	oldKey, newKey := mustKey(t), mustKey(t)
	store, _ := seedSpaces(t, oldKey, "space-a")

	dk, err := keys.GenerateDK()
	if err != nil {
		t.Fatal(err)
	}
	rkOnly, err := keys.WrapWithRK(dk, []byte("recovery-key"), keys.MinArgonParams)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRK("space-half", rkOnly); err != nil {
		t.Fatal(err)
	}

	got, err := rotate.SRW(store, oldKey, newKey)
	if err != nil {
		t.Fatalf("SRW: %v", err)
	}
	if got.Rotated != 1 || got.Skipped != 1 {
		t.Fatalf("result = %+v, want one rotation and one skip", got)
	}
}

func TestSRWRejectsBadKeyPairs(t *testing.T) {
	key := mustKey(t)
	store, _ := seedSpaces(t, key, "space-a")

	cases := map[string]struct{ oldKey, newKey []byte }{
		"identical keys": {key, key},
		"short old key":  {[]byte("short"), mustKey(t)},
		"short new key":  {key, []byte("short")},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := rotate.SRW(store, c.oldKey, c.newKey); err == nil {
				t.Fatal("the rotation was accepted")
			}
		})
	}
}

func seedTarget(t *testing.T, store targets.Store, sealer targets.CredSealer, id string, creds targets.PlainCreds) {
	t.Helper()
	wrapped, version, err := sealer.Seal(creds)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTarget(t.Context(), targets.Target{
		ID: id, Name: id, Endpoint: "s3.example:3900", Bucket: "backups",
		WrappedCreds: wrapped, Version: version,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTWKeepsEveryCredential(t *testing.T) {
	ctx := context.Background()
	oldKey, newKey := mustKey(t), mustKey(t)
	oldSealer, err := targets.NewCredSealer(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	newSealer, err := targets.NewCredSealer(newKey)
	if err != nil {
		t.Fatal(err)
	}

	store := targets.NewMemoryStore()
	want := targets.PlainCreds{AccessKeyID: "AKIA-1", SecretAccessKey: "s3cr3t-1"}
	seedTarget(t, store, oldSealer, "target-a", want)
	seedTarget(t, store, oldSealer, "target-b", targets.PlainCreds{AccessKeyID: "AKIA-2", SecretAccessKey: "s3cr3t-2"})

	got, err := rotate.TW(ctx, store, oldKey, newKey)
	if err != nil {
		t.Fatalf("TW: %v", err)
	}
	if got.Rotated != 2 {
		t.Fatalf("result = %+v, want two rotations", got)
	}

	rotated, err := store.GetTarget(ctx, "target-a")
	if err != nil {
		t.Fatal(err)
	}
	opened, err := newSealer.Open(rotated.WrappedCreds)
	if err != nil {
		t.Fatalf("the rotated credentials do not open with the new key: %v", err)
	}
	if opened != want {
		t.Fatal("rotation changed the stored credentials")
	}
	if _, err := oldSealer.Open(rotated.WrappedCreds); err == nil {
		t.Fatal("the credentials still open with the retired key")
	}
}

func TestTWIsResumable(t *testing.T) {
	ctx := context.Background()
	oldKey, newKey := mustKey(t), mustKey(t)
	oldSealer, err := targets.NewCredSealer(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	newSealer, err := targets.NewCredSealer(newKey)
	if err != nil {
		t.Fatal(err)
	}

	store := targets.NewMemoryStore()
	seedTarget(t, store, oldSealer, "target-old", targets.PlainCreds{AccessKeyID: "A", SecretAccessKey: "b"})
	// As if the process died after re-sealing this one.
	seedTarget(t, store, newSealer, "target-ahead", targets.PlainCreds{AccessKeyID: "C", SecretAccessKey: "d"})

	got, err := rotate.TW(ctx, store, oldKey, newKey)
	if err != nil {
		t.Fatalf("TW: %v", err)
	}
	if got.Rotated != 1 || got.AlreadyRotated != 1 {
		t.Fatalf("result = %+v, want one rotation and one already done", got)
	}

	got, err = rotate.TW(ctx, store, oldKey, newKey)
	if err != nil {
		t.Fatalf("second TW: %v", err)
	}
	if got.Rotated != 0 || got.AlreadyRotated != 2 {
		t.Fatalf("result = %+v, want everything already rotated", got)
	}
}

// Credentials sealed under a third key are not "already rotated" and must not be
// rewritten under an assumption that has already proven wrong.
func TestTWRefusesCredentialsThatOpenWithNeitherKey(t *testing.T) {
	ctx := context.Background()
	oldKey, newKey, otherKey := mustKey(t), mustKey(t), mustKey(t)
	otherSealer, err := targets.NewCredSealer(otherKey)
	if err != nil {
		t.Fatal(err)
	}

	store := targets.NewMemoryStore()
	seedTarget(t, store, otherSealer, "target-foreign", targets.PlainCreds{AccessKeyID: "A", SecretAccessKey: "b"})

	if _, err := rotate.TW(ctx, store, oldKey, newKey); !errors.Is(err, rotate.ErrKeyMismatch) {
		t.Fatalf("err = %v, want ErrKeyMismatch", err)
	}
	stored, err := store.GetTarget(ctx, "target-foreign")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherSealer.Open(stored.WrappedCreds); err != nil {
		t.Fatalf("a refused rotation disturbed the credentials: %v", err)
	}
}
