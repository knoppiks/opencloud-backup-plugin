/**
 * API_LEVEL is the version of /api/v1 this bundle was written against
 * (compatibility-policy.md §2). It must equal the service's
 * `internal/api.APILevel`; a Go test fails when the two differ. The bundle
 * compares it with what GET /version reports and warns on a difference
 * (version/mismatch.ts).
 */
export const API_LEVEL = 1
