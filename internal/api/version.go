package api

import (
	"net/http"

	"github.com/knoppiks/opencloud-backup-plugin/internal/buildinfo"
	"github.com/knoppiks/opencloud-backup-plugin/internal/ocversion"
)

// APILevel is the version of /api/v1 that the service and the web bundle
// agree on (compatibility-policy.md §2). Raise it with any change an older
// or newer bundle would misread: a removed or renamed field or route, a
// changed meaning. Additions an older bundle ignores do not raise it.
//
// The bundle carries the same number (web/src/api/apilevel.ts); a test
// fails when the two differ. A bundle that finds a different level warns
// its users, because its requests may fail.
const APILevel = 1

// openCloudVersion is what GET /api/v1/version reports about OpenCloud. It is
// satisfied by *ocversion.Monitor.
type openCloudVersion interface {
	Status() ocversion.Status
}

// WithOpenCloudVersion sets the source of the OpenCloud version the service
// runs against. Without it the version route reports it as unknown.
func WithOpenCloudVersion(v openCloudVersion) Option {
	return func(s *Server) { s.openCloud = v }
}

// openCloudDTO is the `opencloud` member of GET /api/v1/version. The admin
// view shows a notice when Known && !InWindow (compatibility-policy.md §3).
type openCloudDTO struct {
	Known    bool   `json:"known"`
	Version  string `json:"version,omitempty"`
	Edition  string `json:"edition,omitempty"`
	InWindow bool   `json:"in_window"`
	// Supported is the window in words, the same sentence the README states.
	Supported string `json:"supported"`
}

// versionDTO is GET /api/v1/version: the service's own build, the API
// level it speaks, and the OpenCloud it runs against.
type versionDTO struct {
	Version   string       `json:"version"`
	Commit    string       `json:"commit,omitempty"`
	APILevel  int          `json:"api_level"`
	OpenCloud openCloudDTO `json:"opencloud"`
}

// handleVersion answers any signed-in user. Nothing here is a secret —
// OpenCloud serves its version to anyone at /status.php — but the route stays
// behind authentication like the rest of /api/v1, and /healthz stays
// content-free.
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	st := s.openCloudStatus()
	build := s.build()
	writeJSON(w, http.StatusOK, versionDTO{
		Version:  build.Version,
		Commit:   build.Commit,
		APILevel: APILevel,
		OpenCloud: openCloudDTO{
			Known:     st.Known,
			Version:   st.Version,
			Edition:   st.Edition,
			InWindow:  st.InWindow,
			Supported: st.Window.String(),
		}})
}

// build is the running build, or the one a test set.
func (s *Server) build() buildinfo.Info {
	if s.buildInfo != nil {
		return *s.buildInfo
	}
	return buildinfo.Get()
}

// WithBuildInfo replaces the running build's info, for tests.
func WithBuildInfo(info buildinfo.Info) Option {
	return func(s *Server) { s.buildInfo = &info }
}

// openCloudStatus is the monitor's answer, or — with no monitor wired — an
// unknown version against the window this binary was built with.
func (s *Server) openCloudStatus() ocversion.Status {
	if s.openCloud != nil {
		return s.openCloud.Status()
	}
	// The embedded pins are validated by ocversion's tests, so this cannot
	// fail in a build that passed them.
	pins, err := ocversion.Embedded()
	if err != nil {
		return ocversion.Status{}
	}
	return ocversion.Status{Window: pins.Window()}
}
