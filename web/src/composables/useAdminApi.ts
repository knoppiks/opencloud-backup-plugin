// The admin client, bound to the signed-in session. See useBackupApi.ts: the
// same token handling, a different set of routes, and only the admin chunk
// imports this file.

import { useAuthStore } from '@opencloud-eu/web-pkg'
import { apiBaseUrl } from '../appconfig'
import { AdminApi } from '../api/admin'

/** useAdminApi returns an admin client that reads the token per request. */
export function useAdminApi(): AdminApi {
  const authStore = useAuthStore()
  return new AdminApi({
    baseUrl: apiBaseUrl(),
    getToken: () => authStore.accessToken
  })
}
