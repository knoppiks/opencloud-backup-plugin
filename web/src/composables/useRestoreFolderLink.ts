// A restore folder, as a route into the host's Files app.
//
// `@opencloud-eu/web-pkg` is a host singleton (see useBackupApi.ts), so its
// spaces store and route helpers cost this bundle nothing. The route is a
// plain location object handed to `router-link`; no `vue-router` value is
// imported.

import { createFileRouteOptions, createLocationSpaces, useSpacesStore } from '@opencloud-eu/web-pkg'
import type { SpaceResource } from '@opencloud-eu/web-client'
import { restoreFolderLocation } from '../restore/folderlink'

/** FolderLocation is what `router-link`'s `to` accepts. */
export type FolderLocation = ReturnType<typeof createLocationSpaces>

/**
 * useRestoreFolderLink returns a function from a Space id and its restore
 * folder to a Files-app route, or undefined when the host does not know that
 * Space — in which case the caller names the folder instead of linking it.
 */
export function useRestoreFolderLink(): (
  spaceId: string,
  folder: string
) => FolderLocation | undefined {
  const spacesStore = useSpacesStore()
  return (spaceId, folder) =>
    restoreFolderLocation<SpaceResource, FolderLocation>(
      folder,
      () => spacesStore.getSpace(spaceId) ?? undefined,
      (space, path) =>
        createLocationSpaces('files-spaces-generic', createFileRouteOptions(space, { path }))
    )
}
