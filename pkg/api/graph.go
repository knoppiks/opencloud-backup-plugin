// Shared access to the OpenCloud graph API's /me document.
//
// The OIDC access token on OpenCloud 7.3.0 carries neither a role claim nor a
// groups claim (phase-0-findings.md, admin-role spike), so both admin status
// (decisions.md #13) and group membership (remediation R3) have to be read from
// the graph API with the caller's own bearer token. One fetcher serves both.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Graph $expand selectors. Requesting only what a caller needs keeps the
// response small; OpenCloud accepts them combined as a comma-separated list.
const (
	expandAppRoles = "appRoleAssignments"
	expandMemberOf = "memberOf"
)

// graphMe is the subset of the graph /me document this service reads.
type graphMe struct {
	AppRoleAssignments []struct {
		AppRoleID string `json:"appRoleId"`
	} `json:"appRoleAssignments"`
	MemberOf []struct {
		ID string `json:"id"`
	} `json:"memberOf"`
}

// graphMeFetcher fetches the caller's own graph /me document.
type graphMeFetcher struct {
	// baseURL is the OpenCloud base URL, e.g. https://cloud.example.org.
	baseURL string
	// client is the HTTP client used for the call.
	client *http.Client
}

func newGraphMeFetcher(baseURL string, client *http.Client) graphMeFetcher {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return graphMeFetcher{baseURL: strings.TrimRight(baseURL, "/"), client: client}
}

// fetch calls GET {base}/graph/v1.0/me?$expand={expand} as the caller. Errors
// carry the operation and status only — never the token, never the body.
func (g graphMeFetcher) fetch(ctx context.Context, op string, id Identity, expand string) (graphMe, error) {
	var me graphMe
	if id.Token == "" {
		return me, fmt.Errorf("%s: no caller token", op)
	}
	u := g.baseURL + "/graph/v1.0/me?$expand=" + expand
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return me, fmt.Errorf("%s: %w", op, err)
	}
	req.Header.Set("Authorization", "Bearer "+id.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return me, fmt.Errorf("%s: %w", op, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return me, fmt.Errorf("%s: status %d", op, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return me, fmt.Errorf("%s: decode: %w", op, err)
	}
	return me, nil
}
