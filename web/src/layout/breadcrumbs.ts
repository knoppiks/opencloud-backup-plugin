// The breadcrumb trails every page shows in its header (Phase 8g decision 1).
//
// The trail is also the page's title: its last crumb is the current page,
// drawn bold by the host's breadcrumb like the Files app's header, and
// repeated as the page's (visually hidden) `h1`. Building the trails here
// keeps them the same on every page and testable without mounting one.
//
// Route names, never paths with key material: a Space id and a target id are
// the only parameters, as in every route (src/index.ts).

import type { RouteLocationRaw } from 'vue-router'

/** Crumb is one breadcrumb item, in the shape the host's breadcrumb takes. */
export interface Crumb {
  text: string
  /** to is absent on the current page: it is not a link. */
  to?: RouteLocationRaw
}

/** Gettext is the one gettext function used here. */
type Gettext = (msgid: string) => string

/** spacesCrumbs is the trail of the Spaces overview, as an ancestor or the page. */
export function spacesCrumbs($gettext: Gettext, current = false): Crumb[] {
  const text = $gettext('Spaces')
  return [current ? { text } : { text, to: { name: 'backup-vault-overview' } }]
}

/**
 * spaceCrumbs is the trail down to one Space, then any pages below it. The
 * Space's name may not be loaded yet; a neutral word stands in until it is.
 * With no `pages`, the Space itself is the current page.
 */
export function spaceCrumbs(
  $gettext: Gettext,
  spaceId: string,
  spaceName: string | undefined,
  ...pages: Crumb[]
): Crumb[] {
  const text = spaceName || $gettext('Space')
  const space: Crumb =
    pages.length === 0
      ? { text }
      : { text, to: { name: 'backup-vault-space', params: { spaceId } } }
  return [...spacesCrumbs($gettext), space, ...pages]
}

/** recoveryKeyCrumb is the Recovery Key page, as an ancestor or the page. */
export function recoveryKeyCrumb($gettext: Gettext, spaceId: string, current = false): Crumb {
  const text = $gettext('Recovery Key')
  return current
    ? { text }
    : { text, to: { name: 'backup-vault-recovery-key', params: { spaceId } } }
}

/**
 * destinationsCrumbs is the trail of the admin destination pages. With no
 * `pages`, the destination list is the current page.
 */
export function destinationsCrumbs($gettext: Gettext, ...pages: Crumb[]): Crumb[] {
  const text = $gettext('Backup destinations')
  const list: Crumb =
    pages.length === 0 ? { text } : { text, to: { name: 'backup-vault-admin-targets' } }
  return [list, ...pages]
}

/** pageTitle is the current page's name: the trail's last crumb. */
export function pageTitle(crumbs: Crumb[]): string {
  return crumbs.at(-1)?.text ?? ''
}
