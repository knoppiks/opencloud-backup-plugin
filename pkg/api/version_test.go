package api

import (
	"net/http"
	"testing"

	"opencloud-backup-plugin/pkg/ocversion"
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
