package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/keys"
)

// testArgon keeps the API tests fast; the ceremony itself is parameter-agnostic
// because envelopes carry their own costs.
var apiTestArgon = keys.ArgonParams{Time: 1, MemoryKiB: 8 * 1024, Lanes: 1, SaltLen: 16}

// keyTestEnv bundles a server wired with a key store and a fake space reader.
type keyTestEnv struct {
	srv      *Server
	store    *keys.MemoryStore
	srwKey   []byte
	wrapper  *keys.SRWWrapper
	spaceIDs []string
}

func newKeyTestEnv(t *testing.T) *keyTestEnv {
	t.Helper()
	srwKey, err := keys.GenerateSRWKey()
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := keys.NewSRWWrapper(srwKey)
	if err != nil {
		t.Fatal(err)
	}
	store := keys.NewMemoryStore()

	val := fakeValidator{tokens: map[string]string{
		"alice-tok": "alice",
		"bob-tok":   "bob",
	}}
	reader := fakeSpaceReader{spaces: []cs3.Space{
		{ID: "space-alice", Name: "Alice", Type: "personal", Owner: "alice"},
		{ID: "space-shared", Name: "Team", Type: "project", Members: map[string]string{
			"alice": "manager", "bob": "editor",
		}},
		{ID: "space-bob", Name: "Bob", Type: "personal", Owner: "bob"},
	}}

	srv := NewServer(
		WithTokenValidator(val),
		WithSpaceReader(reader),
		WithKeyStore(store),
		WithSRWWrapper(wrapper),
	)
	return &keyTestEnv{srv: srv, store: store, srwKey: srwKey, wrapper: wrapper}
}

// clientSetup performs the browser's half of the ceremony: generate RK,
// generate DK, wrap DK under RK. Returns the request body and the secrets the
// test needs to verify with (which never leave the test).
func clientSetup(t *testing.T) (body []byte, dk []byte, rkSecret []byte) {
	t.Helper()
	_, rkSecret, err := keys.GenerateRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	dk, err = keys.GenerateDK()
	if err != nil {
		t.Fatal(err)
	}
	env, err := keys.WrapWithRK(dk, rkSecret, apiTestArgon)
	if err != nil {
		t.Fatal(err)
	}
	body, err = json.Marshal(setupRequest{
		WrappedDKRK: base64.StdEncoding.EncodeToString(env.Blob),
		DataKey:     base64.StdEncoding.EncodeToString(dk),
	})
	if err != nil {
		t.Fatal(err)
	}
	return body, dk, rkSecret
}

func doJSON(srv *Server, method, path, token string, body []byte) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	srv.Handler().ServeHTTP(rec, r)
	return rec
}

func TestKeySetupAndStatus(t *testing.T) {
	env := newKeyTestEnv(t)
	body, dk, rkSecret := clientSetup(t)

	rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/backup/setup", "alice-tok", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup status = %d body=%s", rec.Code, rec.Body.String())
	}

	// The response must be status metadata only — no key material.
	respBody := rec.Body.String()
	assertNoKeyMaterial(t, respBody, dk, rkSecret)

	var st keyStatusResponse
	if err := json.Unmarshal([]byte(respBody), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Configured || !st.HasRK || !st.HasSRW {
		t.Fatalf("expected configured after setup: %+v", st)
	}

	// keystatus agrees and also leaks nothing.
	rec = doJSON(env.srv, http.MethodGet, "/api/v1/spaces/space-alice/backup/keystatus", "alice-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("keystatus = %d", rec.Code)
	}
	assertNoKeyMaterial(t, rec.Body.String(), dk, rkSecret)

	// The server-side SRW wrap really recovers the same DK (unattended path).
	stored, err := env.store.GetSRW("space-alice")
	if err != nil {
		t.Fatal(err)
	}
	got, err := env.wrapper.UnwrapSRW(stored)
	if err != nil {
		t.Fatalf("SRW unwrap: %v", err)
	}
	if !bytes.Equal(got, dk) {
		t.Fatal("SRW-wrapped DK does not match the DK from setup")
	}

	// And the stored RK envelope still opens with the user's RK only.
	rkEnv, err := env.store.GetRK("space-alice")
	if err != nil {
		t.Fatal(err)
	}
	gotRK, err := keys.UnwrapRK(rkEnv, rkSecret)
	if err != nil {
		t.Fatalf("RK unwrap: %v", err)
	}
	if !bytes.Equal(gotRK, dk) {
		t.Fatal("RK-wrapped DK does not match")
	}
}

func TestKeyStatusUnconfiguredSpace(t *testing.T) {
	env := newKeyTestEnv(t)
	rec := doJSON(env.srv, http.MethodGet, "/api/v1/spaces/space-alice/backup/keystatus", "alice-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("keystatus = %d", rec.Code)
	}
	var st keyStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Configured || st.HasRK || st.HasSRW {
		t.Fatalf("unconfigured space must report false: %+v", st)
	}
}

func TestKeySetupRejectsNonMember(t *testing.T) {
	env := newKeyTestEnv(t)
	body, _, _ := clientSetup(t)

	// Bob is not a member of Alice's personal space.
	rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/backup/setup", "bob-tok", body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-member setup = %d, want 403", rec.Code)
	}
	// Nothing was stored.
	if _, err := env.store.GetRK("space-alice"); err == nil {
		t.Fatal("non-member setup must not store anything")
	}
}

func TestKeyEndpointsRejectUnknownSpaceLikeNonMember(t *testing.T) {
	env := newKeyTestEnv(t)
	// An absent space must look exactly like a foreign one — no enumeration.
	for _, path := range []string{
		"/api/v1/spaces/does-not-exist/backup/keystatus",
		"/api/v1/spaces/does-not-exist/backup/recovery-envelope",
	} {
		rec := doJSON(env.srv, http.MethodGet, path, "alice-tok", nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s = %d, want 403", path, rec.Code)
		}
	}
}

func TestKeyEndpointsRequireAuth(t *testing.T) {
	env := newKeyTestEnv(t)
	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/spaces/space-alice/backup/setup"},
		{http.MethodGet, "/api/v1/spaces/space-alice/backup/keystatus"},
		{http.MethodGet, "/api/v1/spaces/space-alice/backup/recovery-envelope"},
	}
	for _, c := range cases {
		rec := doJSON(env.srv, c.method, c.path, "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s = %d, want 401", c.method, c.path, rec.Code)
		}
	}
}

func TestRecoveryEnvelopeSharedSpaceMemberAccess(t *testing.T) {
	// decisions.md #7: any member of a shared space may retrieve the RK-wrapped
	// blob. It is ciphertext; the RK itself stays with the user.
	env := newKeyTestEnv(t)
	body, dk, rkSecret := clientSetup(t)

	if rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-shared/backup/setup", "alice-tok", body); rec.Code != http.StatusCreated {
		t.Fatalf("setup = %d body=%s", rec.Code, rec.Body.String())
	}

	// Bob is also a member -> allowed.
	rec := doJSON(env.srv, http.MethodGet, "/api/v1/spaces/space-shared/backup/recovery-envelope", "bob-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("member retrieval = %d body=%s", rec.Code, rec.Body.String())
	}

	var resp recoveryBlobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.KDF != "argon2id" || resp.ArgonTime == 0 {
		t.Fatalf("expected public KDF parameters: %+v", resp)
	}

	blob, err := base64.StdEncoding.DecodeString(resp.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	// The blob is ciphertext: it must not contain the DK.
	if bytes.Contains(blob, dk) {
		t.Fatal("recovery envelope contains the plaintext DK")
	}
	// But it opens with the user's RK, off-line, with no server involvement.
	got, err := keys.UnwrapRK(keys.WrappedDK{Kind: keys.WrapRK, Blob: blob}, rkSecret)
	if err != nil {
		t.Fatalf("member could not use the envelope with the RK: %v", err)
	}
	if !bytes.Equal(got, dk) {
		t.Fatal("envelope did not yield the original DK")
	}
}

func TestRecoveryEnvelopeDeniedToNonMember(t *testing.T) {
	env := newKeyTestEnv(t)
	body, _, _ := clientSetup(t)
	if rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/backup/setup", "alice-tok", body); rec.Code != http.StatusCreated {
		t.Fatalf("setup = %d", rec.Code)
	}

	rec := doJSON(env.srv, http.MethodGet, "/api/v1/spaces/space-alice/backup/recovery-envelope", "bob-tok", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-member retrieval = %d, want 403", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "envelope") {
		t.Fatalf("denial response must not include envelope data: %s", rec.Body.String())
	}
}

func TestRecoveryEnvelopeNotConfigured(t *testing.T) {
	env := newKeyTestEnv(t)
	rec := doJSON(env.srv, http.MethodGet, "/api/v1/spaces/space-alice/backup/recovery-envelope", "alice-tok", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unconfigured retrieval = %d, want 404", rec.Code)
	}
}

func TestKeySetupRejectsBadPayloads(t *testing.T) {
	env := newKeyTestEnv(t)
	dk, _ := keys.GenerateDK()
	_, rk, _ := keys.GenerateRecoveryKey()
	goodEnv, _ := keys.WrapWithRK(dk, rk, apiTestArgon)
	goodBlob := base64.StdEncoding.EncodeToString(goodEnv.Blob)
	goodDK := base64.StdEncoding.EncodeToString(dk)

	srwEnv, _ := keys.WrapWithSRW(dk, env.srwKey)

	cases := map[string]setupRequest{
		"empty":           {},
		"bad base64 env":  {WrappedDKRK: "!!!not-base64!!!", DataKey: goodDK},
		"bad base64 dk":   {WrappedDKRK: goodBlob, DataKey: "!!!"},
		"short dk":        {WrappedDKRK: goodBlob, DataKey: base64.StdEncoding.EncodeToString([]byte("short"))},
		"not an envelope": {WrappedDKRK: base64.StdEncoding.EncodeToString([]byte("garbage")), DataKey: goodDK},
		"wrong envelope kind": {
			WrappedDKRK: base64.StdEncoding.EncodeToString(srwEnv.Blob),
			DataKey:     goodDK,
		},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			body, _ := json.Marshal(req)
			rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/backup/setup", "alice-tok", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s = %d, want 400 (body=%s)", name, rec.Code, rec.Body.String())
			}
		})
	}

	// Malformed JSON too.
	rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/backup/setup", "alice-tok", []byte("{not json"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed json = %d, want 400", rec.Code)
	}
}

func TestKeyEndpointsUnavailableWithoutKeyService(t *testing.T) {
	val := fakeValidator{tokens: map[string]string{"alice-tok": "alice"}}
	reader := fakeSpaceReader{spaces: []cs3.Space{
		{ID: "space-alice", Type: "personal", Owner: "alice"},
	}}
	srv := NewServer(WithTokenValidator(val), WithSpaceReader(reader))

	rec := doJSON(srv, http.MethodGet, "/api/v1/spaces/space-alice/backup/keystatus", "alice-tok", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("without key store = %d, want 503", rec.Code)
	}
}

// assertNoKeyMaterial fails if any secret appears in a response body, in raw,
// base64, or hex-ish form.
func assertNoKeyMaterial(t *testing.T, body string, secrets ...[]byte) {
	t.Helper()
	for _, s := range secrets {
		if len(s) == 0 {
			continue
		}
		if bytes.Contains([]byte(body), s) {
			t.Fatal("response leaked raw key material")
		}
		if strings.Contains(body, base64.StdEncoding.EncodeToString(s)) {
			t.Fatal("response leaked base64 key material")
		}
	}
}
