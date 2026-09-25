// The extension's own configuration, as delivered by the host.
//
// OpenCloud passes per-app configuration to `defineWebApplication`'s `setup()`
// and nowhere else, while our components are mounted later by the *host's*
// router. So the resolved base URL is captured once, here, by the entry point.
//
// This is deliberately the only module-scoped state in the extension, it holds
// an inert string rather than a live client, and it is written exactly once. The
// alternative — threading `applicationConfig` through provide/inject on an app
// instance we do not own — was rejected because the instance a web app receives
// is the host's, and building the extension on what that instance happens to
// expose is a dependency on an implementation detail rather than on an API.

import { resolveApiBase } from './api'

/** BackupVaultConfig is the shape read from `web/src/manifest.json`'s `config`. */
export interface BackupVaultConfig {
  /**
   * apiPath is the path the ingress routes to backupd, matching its
   * BACKUPD_BASE_PATH. A path only: see api/baseurl.ts for why it can never be
   * an origin.
   */
  apiPath?: string
}

let apiBase: string | undefined

/**
 * configureApi resolves and stores the API base URL for this page load.
 *
 * `origin` is injectable for tests; production passes `window.location.origin`.
 * Throws `ApiPathError` on a misconfigured `apiPath`, which surfaces at app
 * setup — the earliest point at which an operator can be told.
 */
export function configureApi(config: BackupVaultConfig, origin: string): string {
  apiBase = resolveApiBase(config.apiPath, origin)
  return apiBase
}

/** apiBaseUrl returns the configured base URL. */
export function apiBaseUrl(): string {
  if (apiBase === undefined) {
    throw new Error('the Backup Vault API is not configured: configureApi was never called')
  }
  return apiBase
}

/** resetApiConfigForTests clears the captured configuration. */
export function resetApiConfigForTests(): void {
  apiBase = undefined
}
