// Backup configuration and run endpoints (phase-4 deliverable 4).
//
// Authorization rules enforced here:
//   - Every route is space-scoped and checks CS3 membership server-side; a
//     non-member gets 403 and learns nothing about the space.
//   - A Space may only be bound to a target that is *granted* to the caller.
//     The grant check runs server-side via targets.Authorizer; a client-supplied
//     target id is never trusted (decisions.md #12).
//   - Responses carry configuration and run metadata only. No key material, no
//     target credentials, no endpoint or bucket (decisions.md #14).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"opencloud-backup-plugin/pkg/backup"
	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/spacecfg"
)

// maxConfigRequestBytes caps configuration payloads.
const maxConfigRequestBytes = 4 << 10

// backupRunner is the subset of *backup.Runner the API needs, kept as an
// interface so handler tests need no kopia, CS3, or S3.
type backupRunner interface {
	StartBackup(ctx context.Context, spaceID string) (string, error)
}

// configRequest is the client's backup configuration for a Space.
type configRequest struct {
	// TargetID must be a target granted to the caller; validated server-side.
	TargetID string `json:"target_id"`
	// RetentionDays is the time-based keep-within window (decisions.md #10).
	// Zero applies the deep default.
	RetentionDays int `json:"retention_days"`
	// Enabled controls scheduled runs; manual runs work regardless.
	Enabled bool `json:"enabled"`
}

// configResponse mirrors the stored configuration.
type configResponse struct {
	SpaceID       string `json:"space_id"`
	TargetID      string `json:"target_id"`
	RetentionDays int    `json:"retention_days"`
	// Schedule is the cron expression scheduled runs follow.
	Schedule  string `json:"schedule"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// runResponse acknowledges an accepted run.
type runResponse struct {
	JobID   string `json:"job_id"`
	SpaceID string `json:"space_id"`
	State   string `json:"state"`
}

// jobResponse is the client view of one run.
type jobResponse struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	// Trigger says whether a person or the schedule started this run.
	Trigger    string `json:"trigger,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	// FileCount and TotalBytes are the logical counts the run processed.
	FileCount  int64  `json:"file_count,omitempty"`
	TotalBytes int64  `json:"total_bytes,omitempty"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	// SnapshotsDeleted and SnapshotsKept are what a prune run did: how many
	// snapshots aged out of the Space's retention window and how many it still
	// has. Absent for every other kind of run.
	SnapshotsDeleted int `json:"snapshots_deleted,omitempty"`
	SnapshotsKept    int `json:"snapshots_kept,omitempty"`
	// Error is the sanitized message the runner recorded.
	Error string `json:"error,omitempty"`
}

// handleGetBackupConfig returns a Space's backup configuration.
func (s *Server) handleGetBackupConfig(w http.ResponseWriter, r *http.Request) {
	id, spaceID, ok := s.requireRole(w, r, cs3.RoleViewer)
	if !ok {
		return
	}
	_ = id

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
	writeJSON(w, http.StatusOK, toConfigResponse(cfg))
}

// handlePutBackupConfig binds a Space to a granted target and sets its
// retention window.
func (s *Server) handlePutBackupConfig(w http.ResponseWriter, r *http.Request) {
	id, spaceID, ok := s.requireRole(w, r, cs3.RoleEditor)
	if !ok {
		return
	}
	if s.spaceConfigs == nil || s.authorizer == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "backup configuration not available")
		return
	}

	var req configRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConfigRequestBytes))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if req.TargetID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "target_id is required")
		return
	}
	// Zero means "use the default". Anything else has a floor: retention depth
	// is what survives ransomware, and a session is not enough authority to
	// remove it (decisions.md threat model).
	if req.RetentionDays < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "retention_days must not be negative")
		return
	}
	if req.RetentionDays > 0 && time.Duration(req.RetentionDays)*24*time.Hour < spacecfg.MinRetentionWindow {
		writeError(w, http.StatusBadRequest, "bad_request",
			fmt.Sprintf("retention_days must be at least %d", int(spacecfg.MinRetentionWindow.Hours()/24)))
		return
	}

	// The client may name any target id; only a granted one is accepted.
	allowed, err := s.authorizer.MayUse(r.Context(), id.Subject, spaceID, req.TargetID)
	if err != nil || !allowed {
		// Same answer for "not granted" and "no such target": no enumeration.
		writeError(w, http.StatusForbidden, "forbidden", "target not available for this space")
		return
	}

	// Changing the target or retention must not silently drop the Space's
	// schedule: schedules are set through their own endpoint.
	schedule := ""
	if existing, err := s.spaceConfigs.Get(r.Context(), spaceID); err == nil {
		schedule = existing.Schedule
	} else if !isConfigNotFound(err) {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read backup configuration")
		return
	}

	cfg, err := s.spaceConfigs.Put(r.Context(), spacecfg.Config{
		SpaceID:         spaceID,
		TargetID:        req.TargetID,
		RetentionWindow: time.Duration(req.RetentionDays) * 24 * time.Hour,
		Schedule:        schedule,
		Enabled:         req.Enabled,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not store backup configuration")
		return
	}
	writeJSON(w, http.StatusOK, toConfigResponse(cfg))
}

// handleRunBackup triggers a backup run ("backup now"). The run executes in the
// background; the response carries the job id to poll.
func (s *Server) handleRunBackup(w http.ResponseWriter, r *http.Request) {
	_, spaceID, ok := s.requireRole(w, r, cs3.RoleEditor)
	if !ok {
		return
	}
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "backup worker not configured")
		return
	}

	jobID, err := s.runner.StartBackup(r.Context(), spaceID)
	if err != nil {
		writeRunError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, runResponse{
		JobID:   jobID,
		SpaceID: spaceID,
		State:   string(jobs.StateRunning),
	})
}

// handleListRuns returns a Space's run history, newest first. An optional
// ?limit= bounds the response; the store reads only what it returns, so a
// status board asking for five runs costs five reads regardless of how long the
// history is.
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	_, spaceID, ok := s.requireRole(w, r, cs3.RoleViewer)
	if !ok {
		return
	}
	if s.jobStore == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "run history not available")
		return
	}

	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}

	list, err := s.jobStore.ListRecent(r.Context(), spaceID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read run history")
		return
	}
	out := make([]jobResponse, 0, len(list))
	for _, j := range list {
		out = append(out, toJobResponse(j))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

// toJobResponse projects a job record for the client. Everything it carries is
// metadata the caller is already entitled to: no paths, no key material, and
// only the sanitized error the runner recorded.
func toJobResponse(j jobs.Job) jobResponse {
	return jobResponse{
		ID:               j.ID,
		Kind:             string(j.Kind),
		State:            string(j.State),
		Trigger:          string(j.Trigger),
		CreatedAt:        formatTime(j.CreatedAt),
		UpdatedAt:        formatTime(j.UpdatedAt),
		FinishedAt:       formatTime(j.FinishedAt),
		FileCount:        j.FileCount,
		TotalBytes:       j.TotalBytes,
		SnapshotID:       j.SnapshotID,
		SnapshotsDeleted: j.SnapshotsDeleted,
		SnapshotsKept:    j.SnapshotsKept,
		Error:            j.Error,
	}
}

// writeRunError maps the runner's sentinel errors onto status codes without
// exposing internal detail.
func writeRunError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, backup.ErrSpaceNotFound):
		// The caller is a verified member, so this means the worker credential
		// cannot see the space — not something the client can act on.
		writeError(w, http.StatusNotFound, "not_found", "space not found")
	case errors.Is(err, backup.ErrNotConfigured):
		writeError(w, http.StatusConflict, "not_configured", "backup is not configured for this space")
	case errors.Is(err, backup.ErrTargetUnavailable):
		writeError(w, http.StatusConflict, "target_unavailable", "the backup target is unavailable")
	case errors.Is(err, backup.ErrRunInProgress):
		writeError(w, http.StatusConflict, "run_in_progress", "a backup run is already in progress")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "could not start the backup run")
	}
}

func toConfigResponse(c spacecfg.Config) configResponse {
	return configResponse{
		SpaceID:       c.SpaceID,
		TargetID:      c.TargetID,
		RetentionDays: int(c.EffectiveRetentionWindow() / (24 * time.Hour)),
		Schedule:      c.EffectiveSchedule(),
		Enabled:       c.Enabled,
		CreatedAt:     formatTime(c.CreatedAt),
		UpdatedAt:     formatTime(c.UpdatedAt),
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}
