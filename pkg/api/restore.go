// Restore endpoints (Path B, phase-5 deliverable 3).
//
// Authorization rules enforced here:
//   - Both routes are space-scoped and check CS3 membership server-side. Being
//     an OpenCloud admin grants nothing: an admin who is not a member of the
//     Space gets the same 403 as any stranger (decisions.md #2, #15). There is
//     deliberately no admin variant of these endpoints.
//   - A restore never overwrites live data; it lands in "Restore/<timestamp>/"
//     (decisions.md #3). That is the worker's job, not the client's: the client
//     cannot choose a destination.
//   - Responses carry snapshot and job metadata only — no key material, no
//     target credentials, no file names.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/jobs"
	"opencloud-backup-plugin/pkg/restore"
	"opencloud-backup-plugin/pkg/snapshot"
)

// maxRestoreRequestBytes caps restore payloads; they hold one snapshot id.
const maxRestoreRequestBytes = 4 << 10

// restoreRunner is the subset of *restore.Runner the API needs, kept as an
// interface so handler tests need no kopia, CS3, or S3.
type restoreRunner interface {
	ListSnapshots(ctx context.Context, spaceID string) ([]snapshot.Info, error)
	StartRestore(ctx context.Context, spaceID string, id snapshot.SnapshotID) (string, error)
}

// snapshotDTO is one entry in the restore picker.
type snapshotDTO struct {
	ID         string `json:"id"`
	TakenAt    string `json:"taken_at"`
	FileCount  int64  `json:"file_count"`
	TotalBytes int64  `json:"total_bytes"`
}

// restoreRequest names the snapshot to restore.
type restoreRequest struct {
	SnapshotID string `json:"snapshot_id"`
}

// restoreResponse acknowledges an accepted restore.
type restoreResponse struct {
	JobID      string `json:"job_id"`
	SpaceID    string `json:"space_id"`
	SnapshotID string `json:"snapshot_id"`
	State      string `json:"state"`
}

// handleListSnapshots returns the snapshots a member may restore, newest first.
func (s *Server) handleListSnapshots(w http.ResponseWriter, r *http.Request) {
	_, spaceID, ok := s.requireRole(w, r, cs3.RoleViewer)
	if !ok {
		return
	}
	if s.restorer == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "restore is not available")
		return
	}

	infos, err := s.restorer.ListSnapshots(r.Context(), spaceID)
	if err != nil {
		writeRestoreError(w, err)
		return
	}

	out := make([]snapshotDTO, 0, len(infos))
	for _, info := range infos {
		out = append(out, snapshotDTO{
			ID:         string(info.ID),
			TakenAt:    formatTime(info.StartTime),
			FileCount:  info.FileCount,
			TotalBytes: info.TotalBytes,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": out})
}

// handleRestore starts a restore of one snapshot into the Space.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	_, spaceID, ok := s.requireRole(w, r, cs3.RoleViewer)
	if !ok {
		return
	}
	if s.restorer == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "restore is not available")
		return
	}

	var req restoreRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRestoreRequestBytes))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if req.SnapshotID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "snapshot_id is required")
		return
	}

	jobID, err := s.restorer.StartRestore(r.Context(), spaceID, snapshot.SnapshotID(req.SnapshotID))
	if err != nil {
		writeRestoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, restoreResponse{
		JobID:      jobID,
		SpaceID:    spaceID,
		SnapshotID: req.SnapshotID,
		State:      string(jobs.StateRunning),
	})
}

// writeRestoreError maps the runner's sentinel errors onto status codes without
// exposing internal detail.
func writeRestoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, restore.ErrSpaceNotFound):
		writeError(w, http.StatusNotFound, "not_found", "space not found")
	case errors.Is(err, restore.ErrSnapshotNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such snapshot")
	case errors.Is(err, restore.ErrNotConfigured):
		writeError(w, http.StatusConflict, "not_configured", "backup is not configured for this space")
	case errors.Is(err, restore.ErrTargetUnavailable):
		writeError(w, http.StatusConflict, "target_unavailable", "the backup target is unavailable")
	case errors.Is(err, restore.ErrRunInProgress):
		writeError(w, http.StatusConflict, "run_in_progress", "a run is already in progress")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "could not start the restore")
	}
}
