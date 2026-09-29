// The user directory for grant editing, backed by the host's graph client.
// See admin/directory.ts for what it does with it.

import { useCapabilityStore, useClientService } from '@opencloud-eu/web-pkg'
import { graphUserDirectory, type UserDirectory } from '../admin/directory'

/** useUserDirectory searches and names OpenCloud users as the signed-in admin. */
export function useUserDirectory(): UserDirectory {
  return graphUserDirectory(useClientService().graphAuthenticated.users)
}

/**
 * useUserSearchMinLength is how many characters a search needs before it is
 * sent, as the server's sharing capability says. Upstream share dialogs
 * respect the same number.
 */
export function useUserSearchMinLength(): number {
  return useCapabilityStore().sharingSearchMinLength
}
