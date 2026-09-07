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

// apiTestArgon is exactly the policy floor: the cheapest envelope the API
// accepts, so the tests stay as fast as the policy allows while still exercising
// it. Anything weaker is a 400 (see TestKeySetupRejectsWeakRecoveryEnvelope).
var apiTestArgon = keys.MinArgonParams

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
		{ID: "space-shared", Name: "Team", Type: "project", Members: grants(map[string]cs3.Role{
			"alice": cs3.RoleManager, "bob": cs3.RoleEditor,
		})},
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

// The guard this whole endpoint pair exists for: a second ceremony would
// install a new Data Key and silently orphan every snapshot already written
// under the old one.
func TestKeySetupIsRefusedOnceTheSpaceIsConfigured(t *testing.T) {
	env := newKeyTestEnv(t)
	first, firstDK, firstRK := clientSetup(t)

	if rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-shared/backup/setup", "alice-tok", first); rec.Code != http.StatusCreated {
		t.Fatalf("first setup = %d body=%s", rec.Code, rec.Body.String())
	}

	// A second ceremony, by the same manager, with a perfectly valid envelope.
	second, _, _ := clientSetup(t)
	rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-shared/backup/setup", "alice-tok", second)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second setup = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}

	// Both envelopes are exactly what the first ceremony stored: the original
	// Recovery Key still opens the Space, and the server still holds the
	// original Data Key.
	rkEnv, err := env.store.GetRK("space-shared")
	if err != nil {
		t.Fatal(err)
	}
	gotRK, err := keys.UnwrapRK(rkEnv, firstRK)
	if err != nil {
		t.Fatalf("the first Recovery Key no longer opens the space: %v", err)
	}
	if !bytes.Equal(gotRK, firstDK) {
		t.Fatal("the stored recovery envelope wraps a different data key")
	}
	srwEnv, err := env.store.GetSRW("space-shared")
	if err != nil {
		t.Fatal(err)
	}
	gotSRW, err := env.wrapper.UnwrapSRW(srwEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotSRW, firstDK) {
		t.Fatal("the stored server envelope wraps a different data key")
	}
}

// A setup interrupted between the two writes leaves a half-configured Space with
// no snapshots behind it. Re-running the ceremony there destroys nothing, so it
// is allowed — the guard is "both envelopes present", not "any".
func TestKeySetupCompletesAHalfFinishedCeremony(t *testing.T) {
	env := newKeyTestEnv(t)

	dk, err := keys.GenerateDK()
	if err != nil {
		t.Fatal(err)
	}
	_, rk, err := keys.GenerateRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	partial, err := keys.WrapWithRK(dk, rk, apiTestArgon)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.PutRK("space-alice", partial); err != nil {
		t.Fatal(err)
	}

	body, _, _ := clientSetup(t)
	if rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/backup/setup", "alice-tok", body); rec.Code != http.StatusCreated {
		t.Fatalf("setup over a half-finished ceremony = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestKeySetupRejectsWeakRecoveryEnvelope(t *testing.T) {
	env := newKeyTestEnv(t)

	dk, err := keys.GenerateDK()
	if err != nil {
		t.Fatal(err)
	}
	_, rk, err := keys.GenerateRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	weak := keys.MinArgonParams
	weak.MemoryKiB--
	envelope, err := keys.WrapWithRK(dk, rk, weak)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(setupRequest{
		WrappedDKRK: base64.StdEncoding.EncodeToString(envelope.Blob),
		DataKey:     base64.StdEncoding.EncodeToString(dk),
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/backup/setup", "alice-tok", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("weak envelope = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "too weak") {
		t.Fatalf("the client cannot tell what to fix: %s", rec.Body.String())
	}
	if _, err := env.store.GetRK("space-alice"); err == nil {
		t.Fatal("a weak envelope was stored")
	}
}

// Rotation replaces the Recovery Key and nothing else: the Data Key, the server
// envelope, and therefore every existing backup are untouched.
func TestRotateRecoveryKeyKeepsTheDataKey(t *testing.T) {
	env := newKeyTestEnv(t)
	body, dk, oldRK := clientSetup(t)
	if rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-shared/backup/setup", "alice-tok", body); rec.Code != http.StatusCreated {
		t.Fatalf("setup = %d", rec.Code)
	}
	srwBefore, err := env.store.GetSRW("space-shared")
	if err != nil {
		t.Fatal(err)
	}

	// The browser's half: unwrap with the old key, re-wrap the same DK under a
	// new one. The DK never goes back over the wire.
	_, newRK, err := keys.GenerateRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	current, err := env.store.GetRK("space-shared")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := keys.RotateRK(current, oldRK, newRK, apiTestArgon)
	if err != nil {
		t.Fatal(err)
	}
	rotateBody, err := json.Marshal(rotateRecoveryKeyRequest{
		WrappedDKRK: base64.StdEncoding.EncodeToString(rotated.Blob),
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-shared/backup/recovery-key/rotate", "alice-tok", rotateBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate = %d body=%s", rec.Code, rec.Body.String())
	}
	assertNoKeyMaterial(t, rec.Body.String(), dk, oldRK, newRK)

	// The new key opens the space; the old one does not.
	stored, err := env.store.GetRK("space-shared")
	if err != nil {
		t.Fatal(err)
	}
	got, err := keys.UnwrapRK(stored, newRK)
	if err != nil {
		t.Fatalf("the new Recovery Key does not open the space: %v", err)
	}
	if !bytes.Equal(got, dk) {
		t.Fatal("rotation changed the data key")
	}
	if _, err := keys.UnwrapRK(stored, oldRK); err == nil {
		t.Fatal("the old Recovery Key still opens the space")
	}

	// The server's own envelope is untouched, so unattended runs keep working
	// against the existing repository.
	srwAfter, err := env.store.GetSRW("space-shared")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(srwBefore.Blob, srwAfter.Blob) {
		t.Fatal("rotation disturbed the server envelope")
	}
}

func TestRotateRecoveryKeyRejectsBadRequests(t *testing.T) {
	env := newKeyTestEnv(t)
	body, dk, rk := clientSetup(t)
	if rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-shared/backup/setup", "alice-tok", body); rec.Code != http.StatusCreated {
		t.Fatalf("setup = %d", rec.Code)
	}

	weak := keys.MinArgonParams
	weak.Time--
	weakEnv, err := keys.WrapWithRK(dk, rk, weak)
	if err != nil {
		t.Fatal(err)
	}
	srwEnv, err := keys.WrapWithSRW(dk, env.srwKey)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		space, token string
		payload      rotateRecoveryKeyRequest
		want         int
	}{
		"non-member": {
			space: "space-alice", token: "bob-tok",
			payload: rotateRecoveryKeyRequest{WrappedDKRK: base64.StdEncoding.EncodeToString(weakEnv.Blob)},
			want:    http.StatusForbidden,
		},
		"space that was never set up": {
			space: "space-alice", token: "alice-tok",
			payload: rotateRecoveryKeyRequest{WrappedDKRK: base64.StdEncoding.EncodeToString(weakEnv.Blob)},
			want:    http.StatusNotFound,
		},
		"weak envelope": {
			space: "space-shared", token: "alice-tok",
			payload: rotateRecoveryKeyRequest{WrappedDKRK: base64.StdEncoding.EncodeToString(weakEnv.Blob)},
			want:    http.StatusBadRequest,
		},
		"server envelope instead of a recovery one": {
			space: "space-shared", token: "alice-tok",
			payload: rotateRecoveryKeyRequest{WrappedDKRK: base64.StdEncoding.EncodeToString(srwEnv.Blob)},
			want:    http.StatusBadRequest,
		},
		"not an envelope": {
			space: "space-shared", token: "alice-tok",
			payload: rotateRecoveryKeyRequest{WrappedDKRK: base64.StdEncoding.EncodeToString([]byte("garbage"))},
			want:    http.StatusBadRequest,
		},
		"empty": {
			space: "space-shared", token: "alice-tok",
			want: http.StatusBadRequest,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			payload, err := json.Marshal(c.payload)
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/spaces/" + c.space + "/backup/recovery-key/rotate"
			rec := doJSON(env.srv, http.MethodPost, path, c.token, payload)
			if rec.Code != c.want {
				t.Fatalf("%s = %d, want %d (body=%s)", name, rec.Code, c.want, rec.Body.String())
			}
		})
	}

	// Whatever was refused, the space still opens with the key it was set up
	// with.
	stored, err := env.store.GetRK("space-shared")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keys.UnwrapRK(stored, rk); err != nil {
		t.Fatalf("a refused rotation disturbed the stored envelope: %v", err)
	}
}

func TestRotateRecoveryKeyRequiresAuth(t *testing.T) {
	env := newKeyTestEnv(t)
	rec := doJSON(env.srv, http.MethodPost, "/api/v1/spaces/space-alice/backup/recovery-key/rotate", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated rotate = %d, want 401", rec.Code)
	}
}

func TestKeyEndpointsUnavailableWithoutKeyService(t *testing.T) {
	val := fakeValidator{tokens: map[string]string{"alice-tok": "alice"}}
	reader := fakeSpaceReader{spaces: []cs3.Space{
		{ID: "space-alice", Type: "personal", Owner: "alice"},
	}}
	srv := NewServer(WithTokenValidator(val), WithSpaceReader(reader))

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/spaces/space-alice/backup/keystatus"},
		{http.MethodPost, "/api/v1/spaces/space-alice/backup/recovery-key/rotate"},
	} {
		rec := doJSON(srv, c.method, c.path, "alice-tok", nil)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s without key store = %d, want 503", c.path, rec.Code)
		}
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
