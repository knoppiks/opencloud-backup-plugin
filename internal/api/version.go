package api

import (
	"net/http"

	"github.com/knoppiks/opencloud-backup-plugin/internal/ocversion"
)

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
// view shows a notice when Known && !InWindow (compatibility-policy.md §3);
// Phase 11 adds the plugin's own version and API level beside it.
type openCloudDTO struct {
	Known    bool   `json:"known"`
	Version  string `json:"version,omitempty"`
	Edition  string `json:"edition,omitempty"`
	InWindow bool   `json:"in_window"`
	// Supported is the window in words, the same sentence the README states.
	Supported string `json:"supported"`
}

type versionDTO struct {
	OpenCloud openCloudDTO `json:"opencloud"`
}

// handleVersion answers any signed-in user. Nothing here is a secret —
// OpenCloud serves its version to anyone at /status.php — but the route stays
// behind authentication like the rest of /api/v1, and /healthz stays
// content-free.
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	st := s.openCloudStatus()
	writeJSON(w, http.StatusOK, versionDTO{OpenCloud: openCloudDTO{
		Known:     st.Known,
		Version:   st.Version,
		Edition:   st.Edition,
		InWindow:  st.InWindow,
		Supported: st.Window.String(),
	}})
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
