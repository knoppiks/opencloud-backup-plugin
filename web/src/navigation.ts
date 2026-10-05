// The left navigation (Phase 8g decision 1): "Spaces" for everyone and
// "Backup destinations" for administrators, drawn by the host like Files'.
//
// Registered as `sidebarNav` extensions on the app's own extension point, the
// same thing the host does with a `navItems` list. Done by hand because the
// host derives each item's extension id from its `name`, and a translated
// name needs to be a function to follow a language change; an id built from
// a function's source text is not one to depend on.
//
// "Backup destinations" is only *offered* to admins. Every admin view checks
// again, and the server's 403 is what decides (8e decision 1).

import type { ExtensionPoint, SidebarNavExtension } from '@opencloud-eu/web-pkg'
import { isAdmin, type AbilityCheck } from './composables/useIsAdmin'

/** Gettext is the one gettext function used here. */
type Gettext = (msgid: string) => string

/** navExtensionPointId is the id the host reads an app's nav items from. */
export function navExtensionPointId(appId: string): string {
  return `app.${appId}.navItems`
}

/** navExtensionPoint declares where the app's nav items are registered. */
export function navExtensionPoint(appId: string): ExtensionPoint<SidebarNavExtension> {
  return { id: navExtensionPointId(appId), extensionType: 'sidebarNav', multiple: true }
}

/**
 * navExtensions builds the nav items. The host marks an item active when the
 * current URL starts with its route or one of `activeFor`, so "Spaces" stays
 * active on every page below a Space, and "Backup destinations" on the
 * destination pages.
 */
export function navExtensions(
  appId: string,
  $gettext: Gettext,
  ability: AbilityCheck
): SidebarNavExtension[] {
  const extensionPointIds = [navExtensionPointId(appId)]
  return [
    {
      id: `app.${appId}.nav.spaces`,
      type: 'sidebarNav',
      extensionPointIds,
      navItem: {
        name: () => $gettext('Spaces'),
        icon: 'layout-grid',
        route: { name: `${appId}-overview` },
        activeFor: [{ path: `/${appId}/space` }],
        priority: 10
      }
    },
    {
      id: `app.${appId}.nav.destinations`,
      type: 'sidebarNav',
      extensionPointIds,
      navItem: {
        name: () => $gettext('Backup destinations'),
        icon: 'server',
        route: { name: `${appId}-admin-targets` },
        isVisible: () => isAdmin(ability),
        priority: 20
      }
    }
  ]
}
