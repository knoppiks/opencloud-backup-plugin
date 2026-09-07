//go:build integration

package cs3state

// Phase-6 persistence, against a live OpenCloud.
//
// The unit tests prove the store's logic against a fake Space. What they cannot
// prove is the assumption the whole design rests on: that a service account can
// create folders, write, overwrite, list and delete inside a Space over CS3,
// with no transactions and no compare-and-set. That is what this test is for.
//
//	cd test/fixtures/opencloud && ./up.sh && ./seed.sh
//	source test/fixtures/opencloud/fixture.env
//	go test -tags integration -run TestIntegration_CS3State ./pkg/cs3state/...
//
// CS3_STATE_SPACE_ID selects the Space to use. In a real deployment that is a
// dedicated Space no end user belongs to; for the fixture, the seeded Space is
// acceptable because the test cleans up after itself.

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/state"
)

func TestIntegration_CS3State(t *testing.T) {
	addr := os.Getenv("CS3_GATEWAY_ADDR")
	saID := os.Getenv("CS3_SERVICE_ACCOUNT_ID")
	saSecret := os.Getenv("CS3_SERVICE_ACCOUNT_SECRET")
	if addr == "" || saID == "" || saSecret == "" {
		t.Skip("OpenCloud fixture env not set; source test/fixtures/opencloud/fixture.env")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial gateway %s: %v", addr, err)
	}
	// Registered before the state cleanup so it runs after it (cleanups are
	// LIFO): the connection has to outlive the tidy-up that uses it.
	t.Cleanup(func() { _ = conn.Close() })

	gw := gateway.NewGatewayAPIClient(conn)
	client := cs3.NewClient(gw,
		cs3.ServiceAccountAuth{Gateway: gw, ClientID: saID, Secret: saSecret},
		cs3.WithHTTPClient(&http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		}),
	)

	spaceID := os.Getenv("CS3_STATE_SPACE_ID")
	if spaceID == "" {
		spaces, err := client.ListSpaces(ctx)
		if err != nil {
			t.Fatalf("ListSpaces: %v", err)
		}
		if len(spaces) == 0 {
			t.Skip("no space visible to the service account")
		}
		spaceID = spaces[0].ID
	}

	// A unique prefix per run, removed at the end, so repeated runs against a
	// long-lived fixture do not accumulate rubbish.
	prefix := fmt.Sprintf(".backup-service-state-test-%d", time.Now().UnixNano())
	store, err := New(client, Options{SpaceID: spaceID, Prefix: prefix})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		keys, err := store.List(cleanupCtx, "")
		if err != nil {
			t.Logf("cleanup listing failed: %v", err)
			return
		}
		for _, key := range keys {
			if err := store.Delete(cleanupCtx, key); err != nil {
				t.Logf("cleanup of %s failed: %v", key, err)
			}
		}
	})

	t.Run("round trip", func(t *testing.T) {
		if err := store.Create(ctx, "docs/one", []byte(`{"a":1}`)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := store.Get(ctx, "docs/one")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if string(got) != `{"a":1}` {
			t.Fatalf("Get = %q", got)
		}
	})

	// The two properties every durable record depends on: a re-derivable record
	// can be replaced, and a record that must never be lost cannot.
	t.Run("replace", func(t *testing.T) {
		if err := store.Replace(ctx, "docs/one", []byte(`{"a":2}`)); err != nil {
			t.Fatalf("Replace: %v", err)
		}
		got, err := store.Get(ctx, "docs/one")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if string(got) != `{"a":2}` {
			t.Fatalf("Get = %q, want the replacement", got)
		}
	})

	t.Run("create refuses to replace", func(t *testing.T) {
		if err := store.Create(ctx, "docs/one", []byte(`{"a":3}`)); !state.IsExists(err) {
			t.Fatalf("Create over an existing document = %v, want already-exists", err)
		}
		got, err := store.Get(ctx, "docs/one")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if string(got) != `{"a":2}` {
			t.Fatalf("Get = %q, want the document a refused create left alone", got)
		}
	})

	t.Run("list and delete", func(t *testing.T) {
		if err := store.Create(ctx, "jobs/space/1", []byte("{}")); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := store.Create(ctx, "jobs/space/2", []byte("{}")); err != nil {
			t.Fatalf("Create: %v", err)
		}

		keys, err := store.List(ctx, "jobs/space")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(keys) != 2 || keys[0] != "jobs/space/1" || keys[1] != "jobs/space/2" {
			t.Fatalf("keys = %v", keys)
		}

		if err := store.Delete(ctx, "jobs/space/1"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := store.Get(ctx, "jobs/space/1"); !state.IsNotFound(err) {
			t.Fatalf("Get after delete = %v", err)
		}
	})

	// And the thing all of this exists for: run history that outlives the
	// process, keyed by a real OpenCloud space id (which contains "$" and "!").
	t.Run("job history survives a new store instance", func(t *testing.T) {
		clock := testutil.NewFakeClock(time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC))
		before := jobs.NewStateStore(store, clock)

		job, err := before.Create(ctx, jobs.Job{
			SpaceID: spaceID,
			Kind:    jobs.KindBackup,
			State:   jobs.StateRunning,
			Trigger: jobs.TriggerSchedule,
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := before.Finish(ctx, job.ID, jobs.Outcome{State: jobs.StateSucceeded, SnapshotID: "snap-1"}); err != nil {
			t.Fatalf("Finish: %v", err)
		}

		fresh, err := New(client, Options{SpaceID: spaceID, Prefix: prefix})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		after := jobs.NewStateStore(fresh, clock)

		history, err := after.List(ctx, spaceID)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(history) != 1 || history[0].ID != job.ID || history[0].SnapshotID != "snap-1" {
			t.Fatalf("history = %+v", history)
		}
	})

	// The record whose loss cannot be repaired, against the real backend: a
	// second setup appends a version, the previous one is still there, and the
	// newest is what a restarted service reads.
	t.Run("key envelopes are append-only", func(t *testing.T) {
		clock := testutil.NewFakeClock(time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC))
		store := keys.NewStateStore(store, clock)

		if err := store.PutRK(spaceID, keys.WrappedDK{Version: 1, Kind: keys.WrapRK, Blob: []byte("rk-one")}); err != nil {
			t.Fatalf("PutRK: %v", err)
		}
		clock.Advance(time.Hour)
		if err := store.PutRK(spaceID, keys.WrappedDK{Version: 1, Kind: keys.WrapRK, Blob: []byte("rk-two")}); err != nil {
			t.Fatalf("PutRK again: %v", err)
		}

		got, err := store.GetRK(spaceID)
		if err != nil {
			t.Fatalf("GetRK: %v", err)
		}
		if string(got.Blob) != "rk-two" {
			t.Fatalf("GetRK = %q, want the newest envelope", got.Blob)
		}
	})
}

// What reva actually does when a document is uploaded over an existing path.
//
// Pinned against opencloudeu/opencloud-rolling:7.3.0: InitiateFileUpload on an
// existing path **succeeds and overwrites**. It does not return ALREADY_EXISTS.
//
// That is the answer the durability design needed, and it is the dangerous one:
// nothing in the transport stops a write from destroying a stored key envelope,
// so "the upload would have refused" is not a guarantee anything may rely on.
// cs3state.Create therefore checks the folder itself before writing, and this
// test exists so that a change in OpenCloud's behaviour shows up here rather
// than as a lost envelope.
//
// The corollary for Replace: the delete-then-write fallback it carries never
// fires on this version. It is kept for servers that do refuse, at the cost of
// nothing on those that do not.
func TestIntegration_CS3StateOverwriteSemantics(t *testing.T) {
	addr := os.Getenv("CS3_GATEWAY_ADDR")
	saID := os.Getenv("CS3_SERVICE_ACCOUNT_ID")
	saSecret := os.Getenv("CS3_SERVICE_ACCOUNT_SECRET")
	if addr == "" || saID == "" || saSecret == "" {
		t.Skip("OpenCloud fixture env not set; source test/fixtures/opencloud/fixture.env")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial gateway %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	gw := gateway.NewGatewayAPIClient(conn)
	client := cs3.NewClient(gw,
		cs3.ServiceAccountAuth{Gateway: gw, ClientID: saID, Secret: saSecret},
		cs3.WithHTTPClient(&http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		}),
	)

	spaces, err := client.ListSpaces(ctx)
	if err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	if len(spaces) == 0 {
		t.Skip("no space visible to the service account")
	}
	space := spaces[0]

	dir := fmt.Sprintf(".overwrite-semantics-%d", time.Now().UnixNano())
	if err := client.MakeDir(ctx, space, dir); err != nil {
		t.Fatalf("MakeDir: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := client.Delete(cleanupCtx, space, dir); err != nil {
			t.Logf("cleanup of %s failed: %v", dir, err)
		}
	})

	rel := dir + "/document"
	first := []byte("first")
	if err := client.Upload(ctx, space, rel, int64(len(first)), time.Time{}, bytes.NewReader(first)); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	second := []byte("second")
	err = client.Upload(ctx, space, rel, int64(len(second)), time.Time{}, bytes.NewReader(second))
	if errors.Is(err, cs3.ErrAlreadyExists) {
		t.Fatalf("uploading over an existing document was refused.\n" +
			"OpenCloud's behaviour has changed since 7.3.0: cs3state.Replace's delete-then-write " +
			"fallback now carries real traffic, so its non-atomic window is now reachable")
	}
	if err != nil {
		t.Fatalf("Upload over an existing document: %v", err)
	}

	rc, err := client.OpenFile(ctx, space, rel, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "second" {
		t.Fatalf("stored document = %q, want the overwrite to have taken effect", got)
	}

	// And the guard that matters, through the store: a create over an existing
	// document must refuse and leave it alone, on a server that would happily
	// have overwritten it.
	store, err := New(client, Options{SpaceID: space.ID, Prefix: dir + "/state"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := store.Create(ctx, "keyenvelopes/space/rk/1", []byte("envelope")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Create(ctx, "keyenvelopes/space/rk/1", []byte("clobber")); !state.IsExists(err) {
		t.Fatalf("Create over an existing document = %v, want already-exists", err)
	}
	stored, err := store.Get(ctx, "keyenvelopes/space/rk/1")
	if err != nil || string(stored) != "envelope" {
		t.Fatalf("stored envelope = %q (%v), want the refused create to have left it alone", stored, err)
	}
}
