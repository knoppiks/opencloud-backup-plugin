// Where the backup API lives, and why the answer can only ever be a path.
//
// The Go service implements no CORS middleware and no OPTIONS route, and that
// is deliberate rather than unfinished: it is meant to sit behind an ingress on
// OpenCloud's own origin, because a Space's Data Key crosses that listener once
// at key setup. A second origin would be one more place that key travels to.
//
// So the only thing this extension is allowed to configure is the *path* the
// ingress routes to the service. `apiPath` is read from `applicationConfig`
// (supplied by `web/src/manifest.json`'s `config` key, which OpenCloud copies
// into config.json) and resolved against the page's own origin. An absolute URL
// is refused rather than honoured — if the API really does live somewhere else,
// that is a deployment decision which has to be made by adding CORS on purpose,
// not by a config value that silently starts working.

/** DEFAULT_API_PATH matches BACKUPD_BASE_PATH in the shipped manifest. */
export const DEFAULT_API_PATH = '/backup/api/v1'

/** ApiPathError is a rejected `apiPath` — a configuration mistake, not a runtime failure. */
export class ApiPathError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'ApiPathError'
  }
}

/**
 * resolveApiBase turns a configured path into an absolute, same-origin base URL
 * with no trailing slash.
 *
 * `origin` is injectable so this is testable without a DOM; production passes
 * `window.location.origin`.
 */
export function resolveApiBase(apiPath: string | undefined, origin: string): string {
  const path = (apiPath ?? DEFAULT_API_PATH).trim()

  if (path === '') {
    throw new ApiPathError('apiPath is empty: remove it to use the default, or give a path')
  }
  // "//host/x" is protocol-relative and would leave the origin. It is caught
  // here rather than by the URL constructor, which would happily accept it.
  if (path.startsWith('//')) {
    throw new ApiPathError(
      `apiPath must be a path on this origin, not "${path}": the backup API has no CORS, ` +
        'so it must be reachable under the same origin as OpenCloud'
    )
  }
  if (!path.startsWith('/')) {
    throw new ApiPathError(`apiPath must start with "/": got "${path}"`)
  }
  if (path.includes('?') || path.includes('#')) {
    throw new ApiPathError(`apiPath must be a path without a query or fragment: got "${path}"`)
  }

  const resolved = new URL(path, origin)
  // Belt and braces. Nothing above should be able to produce a different
  // origin, and this is the assertion that says so out loud: a future edit that
  // loosens the checks has to defeat this one too.
  if (resolved.origin !== new URL(origin).origin) {
    throw new ApiPathError(
      `apiPath must not change the origin: "${path}" resolves to ${resolved.origin}`
    )
  }

  return resolved.origin + stripTrailingSlash(resolved.pathname)
}

function stripTrailingSlash(path: string): string {
  return path.endsWith('/') ? path.slice(0, -1) : path
}
