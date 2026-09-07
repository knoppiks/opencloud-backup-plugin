// Schedule, status and notification endpoints (phase-6 deliverable 3).
//
// Authorization is the same rule as everywhere space-scoped: CS3 membership,
// checked server-side, with no admin variant. An OpenCloud admin who is not a
// member of the Space gets the same 403 as a stranger — the admin is a
// configuration actor, not a data actor (decisions.md #15), and a Space's
// backup status is data about that Space.
//
// Responses carry schedules, states, counts and timestamps. Never key material,
// never target credentials, never endpoints, buckets or file names.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/notify"
	"opencloud-backup-plugin/pkg/scheduler"
	"opencloud-backup-plugin/pkg/spacecfg"
)

// maxScheduleRequestBytes caps schedule payloads.
const maxScheduleRequestBytes = 4 << 10

// defaultHistoryLimit bounds an unqualified history request, and maxHistoryLimit
// bounds a greedy one: a client cannot ask the store for a year of runs in one
// response.
const (
	defaultHistoryLimit = 50
	maxHistoryLimit     = 200
)

// scheduleAdvisor answers when a Space next runs. It is satisfied by
// *scheduler.Scheduler, so the status board and the scheduler cannot disagree
// about what "next run" means.
type scheduleAdvisor interface {
	NextRun(ctx context.Context, spaceID string) (time.Time, error)
}

// notificationReader serves a Space's notifications. It is satisfied by
// *notify.StateStore. Only the space-scoped list is reachable from here: there
// is deliberately no route to the operator's events on a space-scoped path.
type notificationReader interface {
	List(ctx context.Context, spaceID string, limit int) ([]notify.Event, error)
}

// scheduleRequest sets a Space's schedule. A client sends either a preset (what
// the UI offers) or a cron expression (what an operator may prefer); presets
// win when both are present, because that is the one a human picked.
type scheduleRequest struct {
	// Enabled turns scheduled runs on or off. Manual runs are unaffected.
	Enabled bool `json:"enabled"`
	// Preset is the family-legible form: daily/weekly plus a time.
	Preset *scheduler.Preset `json:"preset,omitempty"`
	// Cron is the raw expression, for schedules no preset expresses.
	Cron string `json:"cron,omitempty"`
}

// scheduleResponse describes a Space's schedule in both representations, so a
// UI can show the preset it recognises and the cron it does not.
type scheduleResponse struct {
	SpaceID string `json:"space_id"`
	Enabled bool   `json:"enabled"`
	Cron    string `json:"cron"`
	// Preset is "custom" when the cron expression is not one the presets emit.
	Preset scheduler.Preset `json:"preset"`
}

// statusResponse is the status board's payload.
type statusResponse struct {
	SpaceID string `json:"space_id"`
	// Configured reports whether the Space is bound to a target at all.
	Configured bool             `json:"configured"`
	Enabled    bool             `json:"enabled"`
	Cron       string           `json:"cron,omitempty"`
	Preset     scheduler.Preset `json:"preset,omitzero"`
	// Running reports whether a run is under way right now, and which.
	Running      bool         `json:"running"`
	CurrentJob   *jobResponse `json:"current_job,omitempty"`
	LastRun      *jobResponse `json:"last_run,omitempty"`
	LastSuccess  *jobResponse `json:"last_successful_run,omitempty"`
	NextRun      string       `json:"next_run,omitempty"`
	LastError    string       `json:"last_error,omitempty"`
	RetentionDay int          `json:"retention_days,omitempty"`
}

// handlePutSchedule sets a Space's schedule.
func (s *Server) handlePutSchedule(w http.ResponseWriter, r *http.Request) {
	_, spaceID, ok := s.requireRole(w, r, cs3.RoleEditor)
	if !ok {
		return
	}
	if s.spaceConfigs == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "backup configuration not available")
		return
	}

	var req scheduleRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxScheduleRequestBytes))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	cron, err := resolveSchedule(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	// A schedule only means something once the Space is bound to a target, and
	// that binding is where the grant check lives (decisions.md #12). Setting a
	// schedule must not become a second, unchecked way to configure backup.
	cfg, err := s.spaceConfigs.Get(r.Context(), spaceID)
	if err != nil {
		var notFound spacecfg.ErrNotFound
		if errors.As(err, &notFound) {
			writeError(w, http.StatusConflict, "not_configured", "backup is not configured for this space")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read backup configuration")
		return
	}

	cfg.Schedule = cron
	cfg.Enabled = req.Enabled
	stored, err := s.spaceConfigs.Put(r.Context(), cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not store the schedule")
		return
	}
	writeJSON(w, http.StatusOK, toScheduleResponse(stored))
}

// handleGetSchedule returns a Space's schedule.
func (s *Server) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	_, spaceID, ok := s.requireRole(w, r, cs3.RoleViewer)
	if !ok {
		return
	}
	if s.spaceConfigs == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "backup configuration not available")
		return
	}

	cfg, err := s.spaceConfigs.Get(r.Context(), spaceID)
	if err != nil {
		var notFound spacecfg.ErrNotFound
		if errors.As(err, &notFound) {
			writeError(w, http.StatusNotFound, "not_found", "backup is not configured for this space")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read backup configuration")
		return
	}
	writeJSON(w, http.StatusOK, toScheduleResponse(cfg))
}

// handleBackupStatus serves the status board: what happened last, what is
// happening now, what happens next.
func (s *Server) handleBackupStatus(w http.ResponseWriter, r *http.Request) {
	_, spaceID, ok := s.requireRole(w, r, cs3.RoleViewer)
	if !ok {
		return
	}
	if s.spaceConfigs == nil || s.jobStore == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "backup status not available")
		return
	}

	out := statusResponse{SpaceID: spaceID}

	cfg, err := s.spaceConfigs.Get(r.Context(), spaceID)
	switch {
	case err == nil:
		out.Configured = true
		out.Enabled = cfg.Enabled
		out.Cron = cfg.EffectiveSchedule()
		out.Preset = scheduler.PresetOf(out.Cron)
		out.RetentionDay = int(cfg.EffectiveRetentionWindow() / (24 * time.Hour))
	case isConfigNotFound(err):
		// An unconfigured Space is a normal answer, not an error: the UI shows
		// "not set up yet".
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read backup configuration")
		return
	}

	history, err := s.jobStore.ListRecent(r.Context(), spaceID, defaultHistoryLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read run history")
		return
	}
	applyHistory(&out, history)

	if s.schedules != nil && out.Enabled {
		next, err := s.schedules.NextRun(r.Context(), spaceID)
		if err == nil && !next.IsZero() {
			out.NextRun = formatTime(next)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// applyHistory fills the run-derived parts of a status response.
func applyHistory(out *statusResponse, history []jobs.Job) {
	for _, j := range history {
		if !j.State.Terminal() {
			out.Running = true
			current := toJobResponse(j)
			out.CurrentJob = &current
			break
		}
	}
	if last, ok := jobs.LastOf(history, jobs.KindBackup, ""); ok {
		resp := toJobResponse(last)
		out.LastRun = &resp
		if last.State == jobs.StateFailed {
			out.LastError = last.Error
		}
	}
	if success, ok := jobs.LastOf(history, jobs.KindBackup, jobs.StateSucceeded); ok {
		resp := toJobResponse(success)
		out.LastSuccess = &resp
	}
}

// handleListNotifications returns a Space's notifications, newest first. Only
// the Space's own events are reachable here; operator events live in a
// different scope and have no space-scoped route (decisions.md #15).
func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	_, spaceID, ok := s.requireRole(w, r, cs3.RoleViewer)
	if !ok {
		return
	}
	if s.notifications == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "notifications are not available")
		return
	}

	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}

	events, err := s.notifications.List(r.Context(), spaceID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read notifications")
		return
	}

	type notificationDTO struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		Message   string `json:"message"`
		CreatedAt string `json:"created_at"`
	}
	out := make([]notificationDTO, 0, len(events))
	for _, e := range events {
		out = append(out, notificationDTO{
			ID:        e.ID,
			Kind:      string(e.Kind),
			Message:   e.Message,
			CreatedAt: formatTime(e.CreatedAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": out})
}

// resolveSchedule turns a request into the one stored representation.
func resolveSchedule(req scheduleRequest) (string, error) {
	if req.Preset != nil {
		cron, err := req.Preset.Cron()
		if err != nil {
			return "", errors.New("unsupported schedule preset")
		}
		return cron, nil
	}
	if req.Cron == "" {
		// No schedule given: the Space keeps the default nightly run rather
		// than silently never running.
		return "", nil
	}
	if err := scheduler.ValidateCron(req.Cron); err != nil {
		return "", errors.New("schedule is not a valid cron expression")
	}
	return req.Cron, nil
}

// parseLimit reads an optional ?limit=, clamped to a sane maximum.
func parseLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultHistoryLimit, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 {
		writeError(w, http.StatusBadRequest, "bad_request", "limit must be a positive integer")
		return 0, false
	}
	if limit > maxHistoryLimit {
		limit = maxHistoryLimit
	}
	return limit, true
}

func toScheduleResponse(c spacecfg.Config) scheduleResponse {
	cron := c.EffectiveSchedule()
	return scheduleResponse{
		SpaceID: c.SpaceID,
		Enabled: c.Enabled,
		Cron:    cron,
		Preset:  scheduler.PresetOf(cron),
	}
}

func isConfigNotFound(err error) bool {
	var notFound spacecfg.ErrNotFound
	return errors.As(err, &notFound)
}
