package api

// The admin target/grant surface (decisions.md #12, #14, #15; phase-8-web-ui.md
// sub-phase 8b). Until now /api/v1/admin/ answered 404 and the only writer to
// the target store was first-start seeding.
//
// Three rules shape every handler here, and none of them is a preference:
//
//   - Credentials are write-only (#14). They come in on create, update and the
//     connection check, and no response, log line or error message carries one
//     back out. toAdminTargetDTO is the only projection of a stored Target, so
//     "a new route leaked the blob" means "somebody wrote a second projection".
//   - A target's whole CredentialSet is one sealed blob and the admin path may
//     not open it. So credentials are all-or-nothing on write: absent leaves
//     the stored blob untouched, present replaces it entirely. Re-entering
//     only the backup pair drops a configured maintenance pair, and no amount
//     of API design can avoid that without a decrypt the admin may not have.
//   - This is a configuration surface, not a data one (#15). It manages
//     targets and audiences. It reaches no Space, no job and no plaintext, and
//     the one place it needs to know about Spaces — refusing to delete a target
//     still in use — is answered with a count and never with an id.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"opencloud-backup-plugin/pkg/objstore"
	"opencloud-backup-plugin/pkg/targets"
)

// maxAdminRequestBytes caps an admin request body. A target record is a handful
// of short strings; anything larger is a mistake or an attempt.
const maxAdminRequestBytes = 16 << 10

// --- DTOs -------------------------------------------------------------------

// adminTargetDTO is the admin projection of a target. It is deliberately the
// *only* one: a stored Target carries the sealed credential blob, and a handler
// that marshalled one directly would hand it to anyone with an admin session.
//
// Compare targetDTO, the end-user projection, which carries id and name alone
// (decisions.md #12). The admin sees where the backups go; nobody sees the keys
// to get in.
type adminTargetDTO struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Endpoint     string `json:"endpoint"`
	Region       string `json:"region,omitempty"`
	Bucket       string `json:"bucket"`
	Prefix       string `json:"prefix,omitempty"`
	UsePathStyle bool   `json:"use_path_style"`
	DisableTLS   bool   `json:"disable_tls"`
	// MaintenanceConfigured says whether this target holds a separate
	// maintenance credential. A yes/no, read from the record's own metadata —
	// the sealed blob stays sealed.
	MaintenanceConfigured bool      `json:"maintenance_configured"`
	CreatedAt             time.Time `json:"created_at,omitzero"`
	UpdatedAt             time.Time `json:"updated_at,omitzero"`
}

func toAdminTargetDTO(t targets.Target) adminTargetDTO {
	return adminTargetDTO{
		ID:                    t.ID,
		Name:                  t.Name,
		Endpoint:              t.Endpoint,
		Region:                t.Region,
		Bucket:                t.Bucket,
		Prefix:                t.Prefix,
		UsePathStyle:          t.UsePathStyle,
		DisableTLS:            t.DisableTLS,
		MaintenanceConfigured: t.MaintenanceConfigured,
		CreatedAt:             t.CreatedAt,
		UpdatedAt:             t.UpdatedAt,
	}
}

// credentialsRequest is one S3 key pair on the way in. It has no response
// counterpart, and that asymmetry is the point.
type credentialsRequest struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

func (c *credentialsRequest) plain() targets.PlainCreds {
	if c == nil {
		return targets.PlainCreds{}
	}
	return targets.PlainCreds{
		AccessKeyID:     strings.TrimSpace(c.AccessKeyID),
		SecretAccessKey: c.SecretAccessKey,
	}
}

// adminTargetRequest is the create/update/check body.
type adminTargetRequest struct {
	Name         string `json:"name"`
	Endpoint     string `json:"endpoint"`
	Region       string `json:"region"`
	Bucket       string `json:"bucket"`
	Prefix       string `json:"prefix"`
	UsePathStyle bool   `json:"use_path_style"`
	DisableTLS   bool   `json:"disable_tls"`

	// Credentials are the backup role's key pair. Required on create and on a
	// check; optional on update, where absent means "leave the stored ones
	// alone".
	Credentials *credentialsRequest `json:"credentials"`
	// MaintenanceCredentials are the prune role's key pair (decisions.md #9,
	// Tier 2). Optional: without them both roles use Credentials, which is the
	// single-key deployment.
	MaintenanceCredentials *credentialsRequest `json:"maintenance_credentials"`
}

// grantDTO is a grant on the wire. The scope is a word rather than the stored
// integer: an API whose meaning depends on remembering that 2 means "user" is
// one typo away from granting the wrong audience.
type grantDTO struct {
	Scope   string `json:"scope"`
	UserSub string `json:"user_sub,omitempty"`
	SpaceID string `json:"space_id,omitempty"`
}

const (
	scopeWireAllUsers = "all_users"
	scopeWireUser     = "user"
	scopeWireSpace    = "space"
)

func scopeToWire(s targets.GrantScope) string {
	switch s {
	case targets.ScopeAllUsers:
		return scopeWireAllUsers
	case targets.ScopeUser:
		return scopeWireUser
	case targets.ScopeSpace:
		return scopeWireSpace
	default:
		return ""
	}
}

func scopeFromWire(s string) (targets.GrantScope, error) {
	switch s {
	case scopeWireAllUsers:
		return targets.ScopeAllUsers, nil
	case scopeWireUser:
		return targets.ScopeUser, nil
	case scopeWireSpace:
		return targets.ScopeSpace, nil
	default:
		return targets.ScopeUnknown, fmt.Errorf(
			"scope must be one of %q, %q, %q", scopeWireAllUsers, scopeWireUser, scopeWireSpace)
	}
}

// grantsRequest / grantsResponse wrap the list. A target's audience is edited
// as a set, so it travels as one.
type grantsRequest struct {
	Grants []grantDTO `json:"grants"`
}

type grantsResponse struct {
	Grants []grantDTO `json:"grants"`
}

// --- targets ----------------------------------------------------------------

// handleAdminListTargets returns every target, admin projection.
func (s *Server) handleAdminListTargets(w http.ResponseWriter, r *http.Request) {
	if !s.adminTargetsAvailable(w) {
		return
	}
	all, err := s.targetStore.ListTargets(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not list targets")
		return
	}
	// Sorted here rather than trusted from the store: the two implementations
	// order differently, and an admin list that reshuffles between reads is a
	// UI that cannot be tested.
	sort.Slice(all, func(i, j int) bool {
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		return all[i].ID < all[j].ID
	})

	out := make([]adminTargetDTO, 0, len(all))
	for _, t := range all {
		out = append(out, toAdminTargetDTO(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": out})
}

// handleAdminGetTarget returns one target.
func (s *Server) handleAdminGetTarget(w http.ResponseWriter, r *http.Request) {
	if !s.adminTargetsAvailable(w) {
		return
	}
	t, ok := s.loadTarget(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toAdminTargetDTO(t))
}

// handleAdminCreateTarget creates a target from admin-entered configuration and
// credentials, sealing the credentials before anything is stored.
func (s *Server) handleAdminCreateTarget(w http.ResponseWriter, r *http.Request) {
	if !s.adminTargetsAvailable(w) || !s.adminSealerAvailable(w) {
		return
	}
	req, ok := decodeAdminTargetRequest(w, r)
	if !ok {
		return
	}
	if err := req.validate(true); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	wrapped, version, ok := s.sealRequestCredentials(w, req)
	if !ok {
		return
	}

	id, err := newTargetID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not create target")
		return
	}
	now := s.clock()
	created, err := s.targetStore.CreateTarget(r.Context(), targets.Target{
		ID:                    id,
		Name:                  strings.TrimSpace(req.Name),
		Endpoint:              strings.TrimSpace(req.Endpoint),
		Region:                strings.TrimSpace(req.Region),
		Bucket:                strings.TrimSpace(req.Bucket),
		Prefix:                req.Prefix,
		UsePathStyle:          req.UsePathStyle,
		DisableTLS:            req.DisableTLS,
		WrappedCreds:          wrapped,
		Version:               version,
		MaintenanceConfigured: req.MaintenanceCredentials.plain().Complete(),
		CreatedAt:             now,
		UpdatedAt:             now,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not store target")
		return
	}
	writeJSON(w, http.StatusCreated, toAdminTargetDTO(created))
}

// handleAdminUpdateTarget replaces a target's configuration. Credentials left
// out of the body are preserved; credentials supplied replace the whole set.
func (s *Server) handleAdminUpdateTarget(w http.ResponseWriter, r *http.Request) {
	if !s.adminTargetsAvailable(w) {
		return
	}
	existing, ok := s.loadTarget(w, r)
	if !ok {
		return
	}
	req, ok := decodeAdminTargetRequest(w, r)
	if !ok {
		return
	}
	if err := req.validate(false); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	updated := targets.Target{
		ID:           existing.ID,
		Name:         strings.TrimSpace(req.Name),
		Endpoint:     strings.TrimSpace(req.Endpoint),
		Region:       strings.TrimSpace(req.Region),
		Bucket:       strings.TrimSpace(req.Bucket),
		Prefix:       req.Prefix,
		UsePathStyle: req.UsePathStyle,
		DisableTLS:   req.DisableTLS,
		CreatedAt:    existing.CreatedAt,
		UpdatedAt:    s.clock(),
		// Nil WrappedCreds tells the store to keep what it has, which is what
		// makes "edit a target without re-entering its secret" possible while
		// credentials stay write-only.
		MaintenanceConfigured: existing.MaintenanceConfigured,
	}
	if req.Credentials != nil {
		if !s.adminSealerAvailable(w) {
			return
		}
		wrapped, version, sealed := s.sealRequestCredentials(w, req)
		if !sealed {
			return
		}
		updated.WrappedCreds = wrapped
		updated.Version = version
		updated.MaintenanceConfigured = req.MaintenanceCredentials.plain().Complete()
	}

	stored, err := s.targetStore.UpdateTarget(r.Context(), updated)
	if err != nil {
		if isTargetNotFound(err) {
			writeTargetNotFound(w)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "could not store target")
		return
	}
	writeJSON(w, http.StatusOK, toAdminTargetDTO(stored))
}

// handleAdminDeleteTarget removes a target and its grants — unless Spaces are
// still bound to it.
//
// The refusal names a count and never a Space: an admin may know how much work
// deleting this would cause, and may not learn whose (decisions.md #15).
func (s *Server) handleAdminDeleteTarget(w http.ResponseWriter, r *http.Request) {
	if !s.adminTargetsAvailable(w) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeTargetNotFound(w)
		return
	}

	inUse, err := s.countSpacesUsingTarget(r.Context(), id)
	if err != nil {
		// A grant that cannot be evaluated is refused rather than guessed
		// (decisions.md #20), and so is a deletion whose consequences cannot
		// be established.
		writeError(w, http.StatusServiceUnavailable, "unavailable",
			"could not determine whether this target is still in use")
		return
	}
	if inUse > 0 {
		writeError(w, http.StatusConflict, "target_in_use", fmt.Sprintf(
			"%d space(s) still back up to this target; point them elsewhere first", inUse))
		return
	}

	if err := s.targetStore.DeleteTarget(r.Context(), id); err != nil {
		if isTargetNotFound(err) {
			writeTargetNotFound(w)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "could not delete target")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// countSpacesUsingTarget reports how many Space configurations name this
// target. An unreadable configuration is an error, not a zero: it might be the
// one that would have stopped the delete.
func (s *Server) countSpacesUsingTarget(ctx context.Context, targetID string) (int, error) {
	if s.spaceConfigs == nil {
		return 0, errors.New("api: space configuration store not configured")
	}
	configs, unreadable, err := s.spaceConfigs.List(ctx)
	if err != nil {
		return 0, err
	}
	if len(unreadable) > 0 {
		return 0, fmt.Errorf("api: %d unreadable space configuration(s)", len(unreadable))
	}
	count := 0
	for _, c := range configs {
		if c.TargetID == targetID {
			count++
		}
	}
	return count, nil
}

// --- grants -----------------------------------------------------------------

// handleAdminListGrants returns a target's audience.
func (s *Server) handleAdminListGrants(w http.ResponseWriter, r *http.Request) {
	if !s.adminTargetsAvailable(w) {
		return
	}
	t, ok := s.loadTarget(w, r)
	if !ok {
		return
	}
	list, err := s.targetStore.ListGrants(r.Context(), t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read grants")
		return
	}
	writeJSON(w, http.StatusOK, toGrantsResponse(list))
}

// handleAdminReplaceGrants sets a target's whole audience.
//
// Whole-list, because that is how an admin thinks about it and how the store
// writes it: one request, one document, no way for a crash to leave an audience
// nobody chose. An empty list is a legitimate instruction — it revokes the
// target from everyone without deleting it.
func (s *Server) handleAdminReplaceGrants(w http.ResponseWriter, r *http.Request) {
	if !s.adminTargetsAvailable(w) {
		return
	}
	t, ok := s.loadTarget(w, r)
	if !ok {
		return
	}

	var req grantsRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminRequestBytes))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	list := make([]targets.Grant, 0, len(req.Grants))
	for _, g := range req.Grants {
		scope, err := scopeFromWire(g.Scope)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		grant := targets.Grant{
			TargetID: t.ID,
			Scope:    scope,
			UserSub:  strings.TrimSpace(g.UserSub),
			SpaceID:  strings.TrimSpace(g.SpaceID),
		}
		// Validated here as well as in the store so the admin gets a 400 that
		// says which grant is wrong, rather than a 500 that says a write
		// failed.
		if err := grant.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", strings.TrimPrefix(err.Error(), "targets: "))
			return
		}
		list = append(list, grant)
	}

	if err := s.targetStore.ReplaceGrants(r.Context(), t.ID, list); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not store grants")
		return
	}

	stored, err := s.targetStore.ListGrants(r.Context(), t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read grants")
		return
	}
	writeJSON(w, http.StatusOK, toGrantsResponse(stored))
}

func toGrantsResponse(list []targets.Grant) grantsResponse {
	out := make([]grantDTO, 0, len(list))
	for _, g := range list {
		wire := scopeToWire(g.Scope)
		if wire == "" {
			// A stored grant with no scope grants nothing (targets.Grant.
			// Validate); rendering it as a row an admin could "keep" would
			// invite them to write it back.
			continue
		}
		out = append(out, grantDTO{Scope: wire, UserSub: g.UserSub, SpaceID: g.SpaceID})
	}
	return grantsResponse{Grants: out}
}

// --- connection check -------------------------------------------------------

// checkResultDTO is one role's verdict.
type checkResultDTO struct {
	Role    string `json:"role"`
	Outcome string `json:"outcome"`
}

// handleAdminCheckTarget reports whether the submitted configuration and
// credentials can read the submitted bucket.
//
// It is stateless on purpose. Checking a *stored* credential against an
// admin-supplied endpoint would be a credential oracle: change the endpoint to
// a host you control, press the button, and the stored access key id leaves in
// the request's Authorization header. So the check only ever uses what the
// caller already holds, which means an existing target cannot be tested without
// re-entering its secret — which is what write-only credentials mean.
func (s *Server) handleAdminCheckTarget(w http.ResponseWriter, r *http.Request) {
	if s.targetChecker == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "target checks are not available")
		return
	}
	req, ok := decodeAdminTargetRequest(w, r)
	if !ok {
		return
	}
	// A check needs real credentials: the point is to test them.
	if err := req.validate(true); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	results := []checkResultDTO{{
		Role:    string(targets.RoleBackup),
		Outcome: string(s.checkRole(r.Context(), req, req.Credentials.plain())),
	}}
	if maintenance := req.MaintenanceCredentials.plain(); maintenance.Complete() {
		results = append(results, checkResultDTO{
			Role:    string(targets.RoleMaintenance),
			Outcome: string(s.checkRole(r.Context(), req, maintenance)),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Server) checkRole(ctx context.Context, req adminTargetRequest, creds targets.PlainCreds) objstore.CheckOutcome {
	return s.targetChecker.Check(ctx, objstore.S3Config{
		Endpoint:        strings.TrimSpace(req.Endpoint),
		Region:          strings.TrimSpace(req.Region),
		Bucket:          strings.TrimSpace(req.Bucket),
		AccessKeyID:     creds.AccessKeyID,
		SecretAccessKey: creds.SecretAccessKey,
		UsePathStyle:    req.UsePathStyle,
		DisableTLS:      req.DisableTLS,
	}, req.Prefix)
}

// --- helpers ----------------------------------------------------------------

// validate checks a create (requireCredentials) or update body.
//
// The credential rules follow from the sealed set being indivisible: a body may
// carry both pairs, the backup pair alone, or neither (update only). It may not
// carry a maintenance pair without a backup pair, because storing that would
// mean opening the existing blob to keep the half the admin did not send.
func (r adminTargetRequest) validate(requireCredentials bool) error {
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(r.Endpoint) == "" {
		return errors.New("endpoint is required")
	}
	if strings.TrimSpace(r.Bucket) == "" {
		return errors.New("bucket is required")
	}

	backup := r.Credentials.plain()
	maintenance := r.MaintenanceCredentials.plain()

	switch {
	// The more specific diagnosis first: on a create both of the next two
	// apply, and "you cannot set only the maintenance pair" tells the admin
	// what to do next where "credentials are required" does not.
	case r.Credentials == nil && r.MaintenanceCredentials != nil:
		return errors.New(
			"maintenance credentials cannot be set on their own; send both pairs or neither")
	case r.Credentials == nil && requireCredentials:
		return errors.New("credentials are required")
	case r.Credentials != nil && !backup.Complete():
		return errors.New("credentials need both an access key id and a secret access key")
	}

	// Refused rather than ignored, as in first-start seeding: half a
	// maintenance credential would store a target that looks separated and
	// silently uses the backup key for both roles.
	if r.MaintenanceCredentials != nil && !maintenance.Complete() {
		return errors.New(
			"maintenance credentials need both an access key id and a secret access key")
	}
	return nil
}

func decodeAdminTargetRequest(w http.ResponseWriter, r *http.Request) (adminTargetRequest, bool) {
	var req adminTargetRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminRequestBytes))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return adminTargetRequest{}, false
	}
	return req, true
}

// sealRequestCredentials wraps the submitted pairs with the TW key. The error
// path is deliberately mute about what failed: the input was a secret.
func (s *Server) sealRequestCredentials(w http.ResponseWriter, req adminTargetRequest) ([]byte, int, bool) {
	wrapped, version, err := s.credSealer.Seal(targets.CredentialSet{
		Backup:      req.Credentials.plain(),
		Maintenance: req.MaintenanceCredentials.plain(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not store credentials")
		return nil, 0, false
	}
	return wrapped, version, true
}

// loadTarget resolves {id} or writes the 404 itself.
func (s *Server) loadTarget(w http.ResponseWriter, r *http.Request) (targets.Target, bool) {
	id := r.PathValue("id")
	if id == "" {
		writeTargetNotFound(w)
		return targets.Target{}, false
	}
	t, err := s.targetStore.GetTarget(r.Context(), id)
	if err != nil {
		if isTargetNotFound(err) {
			writeTargetNotFound(w)
			return targets.Target{}, false
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read target")
		return targets.Target{}, false
	}
	return t, true
}

func (s *Server) adminTargetsAvailable(w http.ResponseWriter) bool {
	if s.targetStore == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable",
			"target administration is not available")
		return false
	}
	return true
}

// adminSealerAvailable gates the credential-accepting routes only. Without the
// TW key a target cannot be given credentials, but its configuration and its
// audience are still readable and editable — and an operator whose TW_KEY is
// missing needs the admin UI to work in order to see that.
func (s *Server) adminSealerAvailable(w http.ResponseWriter) bool {
	if s.credSealer == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable",
			"target credentials cannot be stored: no target-wrap key is configured")
		return false
	}
	return true
}

func isTargetNotFound(err error) bool {
	var notFound targets.ErrNotFound
	return errors.As(err, &notFound)
}

// writeTargetNotFound is the single 404 body, so a missing target and an
// unknown id read identically.
func writeTargetNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "not_found", "no such target")
}

// newTargetID mints an opaque target id. Opaque rather than admin-chosen: an id
// the admin types is a namespace they have to keep unique, and "create" becomes
// able to overwrite.
func newTargetID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("api: generate target id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
