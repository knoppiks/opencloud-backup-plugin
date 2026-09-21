package api

// Target credentials stay in the cluster (decisions.md #9 Tier 1, #14).
//
// Tier 1 is the layer that actually defeats the threat this project was built
// for: client ransomware cannot reach the backup store because it has no
// credentials. That rests on a claim about this service's API — that no route
// hands a credential to a caller — which was a property of how the handlers
// happened to be written rather than something checked.
//
// It is checked here, over the whole route table: a new route that returns a
// target record, or a widened DTO, fails this test rather than shipping.

import (
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/targets"
)

// sealedRoleTargetCreds seals the marker credentials the role fixture's target
// holds. The TW key is generated per call and thrown away: nothing in these
// tests opens the blob, and the point is that nothing in the service hands it
// to a client either.
func sealedRoleTargetCreds(t *testing.T) []byte {
	t.Helper()

	twKey, err := keys.GenerateSRWKey()
	if err != nil {
		t.Fatalf("GenerateSRWKey: %v", err)
	}
	sealer, err := targets.NewCredSealer(twKey)
	if err != nil {
		t.Fatalf("NewCredSealer: %v", err)
	}
	blob, _, err := sealer.Seal(targets.CredentialSet{
		Backup: targets.PlainCreds{
			AccessKeyID:     roleTargetAccessKeyID,
			SecretAccessKey: roleTargetSecretAccessKey,
		},
		Maintenance: targets.PlainCreds{
			AccessKeyID:     roleTargetMaintenanceID,
			SecretAccessKey: roleTargetMaintenanceKey,
		},
	})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	return blob
}

// credentialMarkers is everything that must never appear in a response.
//
// The plaintext markers catch a handler that unseals and echoes — the shape a
// future admin create/update endpoint could take. The encodings of the sealed
// blob catch the likelier mistake by far: a DTO that simply carries the stored
// Target through, which leaks only ciphertext but leaks it to anyone with a
// session, and is the kind of thing that gets added without anyone noticing.
func credentialMarkers(sealed []byte) []string {
	markers := []string{
		roleTargetAccessKeyID,
		roleTargetSecretAccessKey,
		roleTargetMaintenanceID,
		roleTargetMaintenanceKey,
	}
	if len(sealed) > 0 {
		markers = append(markers,
			base64.StdEncoding.EncodeToString(sealed),
			base64.RawURLEncoding.EncodeToString(sealed),
			hex.EncodeToString(sealed),
		)
	}
	return markers
}

// TestNoRouteEverReturnsATargetCredential calls every space-scoped route as the
// most privileged caller there is — a manager, who is as far as authorization
// goes — and the target list besides, asserting no response body carries a
// credential.
//
// The manager is deliberate: a test that only proved a viewer cannot see
// credentials would be testing the role model, not this property. Credentials
// are write-only for *everyone*, including the people who may change the
// configuration that uses them.
func TestNoRouteEverReturnsATargetCredential(t *testing.T) {
	for _, r := range roleTable() {
		t.Run(r.name, func(t *testing.T) {
			env := newRoleTestEnv(t)
			rec := env.call(r.method, r.path, "manager", r.body)
			assertNoCredential(t, r.name, rec.Body.String(), env.sealedCreds)
		})
	}

	t.Run("list targets", func(t *testing.T) {
		env := newRoleTestEnv(t)
		rec := doJSON(env.srv, http.MethodGet, "/api/v1/targets", roleTestToken+"manager", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /targets = %d: %s", rec.Code, rec.Body.String())
		}
		// The route must have found the target, or the assertion below proves
		// nothing about a response that happens to be empty.
		if !strings.Contains(rec.Body.String(), roleTargetID) {
			t.Fatalf("the granted target was not listed: %s", rec.Body.String())
		}
		assertNoCredential(t, "list targets", rec.Body.String(), env.sealedCreds)
	})
}

// The admin surface is the one an operator reaches with the most authority, and
// decision #15 makes it a configuration surface rather than a data one. It
// answers 404 today; when it grows handlers, this row keeps its promise.
func TestAdminRoutesReturnNoCredential(t *testing.T) {
	env := newRoleTestEnv(t)
	rec := doJSON(env.srv, http.MethodGet, "/api/v1/admin/targets", roleTestToken+"manager", nil)
	assertNoCredential(t, "admin targets", rec.Body.String(), env.sealedCreds)
}

// The public projection of a target is what a UI is given. It carries an id and
// a name, and nothing that describes where the backups live.
func TestPublicViewCarriesNoLocationOrCredential(t *testing.T) {
	target := targets.Target{
		ID: "t1", Name: "Buddy",
		Endpoint: "garage.internal:3900", Bucket: "household-backups",
		WrappedCreds: sealedRoleTargetCreds(t),
	}
	view := target.Public()

	if view.ID != "t1" || view.Name != "Buddy" {
		t.Fatalf("public view = %+v", view)
	}
	rendered := view.ID + " " + view.Name
	unwantedInAView := append(credentialMarkers(target.WrappedCreds), target.Endpoint, target.Bucket)
	for _, unwanted := range unwantedInAView {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("the public view of a target disclosed %q", unwanted)
		}
	}
}

func assertNoCredential(t *testing.T, route, body string, sealed []byte) {
	t.Helper()
	for _, marker := range credentialMarkers(sealed) {
		if strings.Contains(body, marker) {
			t.Fatalf("%s returned a target credential in its response body", route)
		}
	}
}
