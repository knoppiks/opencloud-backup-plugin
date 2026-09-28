// The bridge between OpenCloud's session and our API client.
//
// `@opencloud-eu/web-pkg` and `pinia` are both module-federation singletons
// provided by the host, so importing runtime symbols from them is free. That is
// not true of `vue-router` or `@opencloud-eu/design-system`: importing values
// from those would bundle a second copy into this remote, so the router is
// reached through global properties and design-system components are used as
// globals in templates.

import { useAuthStore } from '@opencloud-eu/web-pkg'
import { apiBaseUrl } from '../appconfig'
import { BackupApi } from '../api'

/**
 * useBackupApi returns a client bound to the signed-in user's session.
 *
 * The token is read per request rather than captured here: the auth store
 * renews it, and a copy taken at construction would eventually 401 every call
 * until the page was reloaded.
 */
export function useBackupApi(): BackupApi {
  const authStore = useAuthStore()
  return new BackupApi({
    baseUrl: apiBaseUrl(),
    getToken: () => authStore.accessToken
  })
}
