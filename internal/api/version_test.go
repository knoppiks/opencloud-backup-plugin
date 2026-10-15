package api

import (
	"net/http"
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/knoppiks/opencloud-backup-plugin/internal/buildinfo"
	"github.com/knoppiks/opencloud-backup-plugin/internal/ocversion"
)

type fixedOpenCloud ocversion.Status

func (f fixedOpenCloud) Status() ocversion.Status { return ocversion.Status(f) }

func testWindow(t *testing.T) ocversion.Window {
	t.Helper()
	pins, err := ocversion.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	return pins.Window()
}

func getVersion(t *testing.T, srv *Server) openCloudDTO {
	t.Helper()
	rec := authGet(srv, "/api/v1/version", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got versionDTO
	decodeBody(t, rec.Body, &got)
	return got.OpenCloud
}

func TestVersion_RequiresAuthentication(t *testing.T) {
	srv := NewServer(WithTokenValidator(fakeValidator{tokens: map[string]string{"tok": "u"}}))
	if rec := authGet(srv, "/api/v1/version", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestVersion_ReportsTheMonitor(t *testing.T) {
	w := testWindow(t)
	for _, tt := range []struct {
		name   string
		status ocversion.Status
	}{
		{"inside", ocversion.Status{Known: true, Version: "7.3.0", Edition: "rolling", InWindow: true, Window: w}},
		{"outside", ocversion.Status{Known: true, Version: "99.0.0", Edition: "rolling", InWindow: false, Window: w}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := NewServer(
				WithTokenValidator(fakeValidator{tokens: map[string]string{"tok": "u"}}),
				WithOpenCloudVersion(fixedOpenCloud(tt.status)),
			)
			got := getVersion(t, srv)
			want := openCloudDTO{
				Known: true, Version: tt.status.Version, Edition: "rolling",
				InWindow: tt.status.InWindow, Supported: w.String(),
			}
			if got != want {
				t.Errorf("got %+v, want %+v", got, want)
			}
		})
	}
}

// Without a monitor (no OC_BASE_URL) the version is unknown, but the window
// the build supports is still worth stating.
func TestVersion_UnknownWithoutMonitor(t *testing.T) {
	srv := NewServer(WithTokenValidator(fakeValidator{tokens: map[string]string{"tok": "u"}}))
	got := getVersion(t, srv)
	if got.Known || got.InWindow || got.Version != "" {
		t.Errorf("got %+v, want unknown", got)
	}
	if got.Supported != testWindow(t).String() {
		t.Errorf("supported = %q", got.Supported)
	}
}

func TestVersion_ReportsTheBuildAndAPILevel(t *testing.T) {
	srv := NewServer(
		WithTokenValidator(fakeValidator{tokens: map[string]string{"tok": "u"}}),
		WithBuildInfo(buildinfo.Info{Version: "v1.2.3", Commit: "0123456789abcdef"}),
	)
	rec := authGet(srv, "/api/v1/version", "tok")
	var got versionDTO
	decodeBody(t, rec.Body, &got)
	if got.Version != "v1.2.3" || got.Commit != "0123456789abcdef" || got.APILevel != APILevel {
		t.Errorf("got %+v", got)
	}
}

// The running binary reports what buildinfo says, "dev" when unstamped.
func TestVersion_DefaultsToTheRunningBuild(t *testing.T) {
	srv := NewServer(WithTokenValidator(fakeValidator{tokens: map[string]string{"tok": "u"}}))
	rec := authGet(srv, "/api/v1/version", "tok")
	var got versionDTO
	decodeBody(t, rec.Body, &got)
	if got.Version != buildinfo.Get().Version {
		t.Errorf("version = %q, want %q", got.Version, buildinfo.Get().Version)
	}
}

// The bundle carries the API level as a constant of its own. Both are
// written by hand; this keeps them equal.
func TestAPILevelMatchesTheWebBundle(t *testing.T) {
	data, err := os.ReadFile("../../web/src/api/apilevel.ts")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^export const API_LEVEL = (\d+)$`).FindAllSubmatch(data, -1)
	if len(m) != 1 {
		t.Fatalf("web/src/api/apilevel.ts: want exactly one `export const API_LEVEL = <n>`, found %d", len(m))
	}
	if got := string(m[0][1]); got != strconv.Itoa(APILevel) {
		t.Errorf("web bundle API_LEVEL = %s, service APILevel = %d", got, APILevel)
	}
}
