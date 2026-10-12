//go:build integration

package ocversion

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/knoppiks/opencloud-backup-plugin/internal/testutil"
)

// TestIntegration_FixtureVersion reads the fixture's version the way backupd
// does — through the single origin, with SSL_CERT_FILE trusting the proxy's CA
// — and checks it against the leg up.sh started. A pinned leg must be inside
// the window this build carries; that is what "supported means CI runs it"
// looks like from the service's side. The canary is only required to answer.
//
//	source test/fixtures/opencloud/fixture.env && go test -tags integration ./internal/ocversion/...
func TestIntegration_FixtureVersion(t *testing.T) {
	env := testutil.OpenCloudEnv(t, "OC_BASE_URL", "OC_FIXTURE_LEG", "OC_FIXTURE_IMAGE")
	base, leg, image := env[0], env[1], env[2]

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pins, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	m := NewMonitor(StatusSource{BaseURL: base, Client: &http.Client{Timeout: 15 * time.Second}},
		pins.Window(), nil)
	if err := m.Check(ctx); err != nil {
		t.Fatalf("read the version of %s: %v", image, err)
	}
	st := m.Status()
	t.Logf("leg %s (%s): OpenCloud %s, edition %s, in window: %v", leg, image, st.Version, st.Edition, st.InWindow)

	if leg == pins.Canary.Name {
		return
	}
	// image is "repo:tag@digest"; the tag is the release it claims to be.
	tag := strings.SplitN(strings.SplitN(image, "@", 2)[0], ":", 2)[1]
	if st.Version != tag {
		t.Errorf("status.php reports %s, the pinned tag is %s", st.Version, tag)
	}
	if !st.InWindow {
		t.Errorf("pinned leg %s (%s) is outside the window %s", leg, st.Version, st.Window)
	}
}
