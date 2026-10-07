// What the admin view says when OpenCloud runs a version this release of
// Backup Vault was not tested with (compatibility-policy.md §3).
//
// The service warns and keeps backing up; it never refuses. The notice says
// the same: nothing is broken yet, and the fix is a newer Backup Vault, not an
// older OpenCloud. Members are not told: there is nothing they can do.

import type { OpenCloudVersion } from '../api'
import type { Gettext } from '../api/errortext'

/** Notice is a title and one sentence of advice. */
export interface Notice {
  title: string
  message: string
}

/**
 * openCloudNotice is the warning to show, or undefined when there is nothing
 * to say: inside the window, not known yet, or not answered at all.
 */
export function openCloudNotice(
  v: OpenCloudVersion | undefined,
  $gettext: Gettext
): Notice | undefined {
  if (!v || !v.known || v.in_window) {
    return undefined
  }
  return {
    title: $gettext('This OpenCloud version has not been tested with Backup Vault'),
    message: $gettext(
      'OpenCloud %{version} is running; this release of Backup Vault was tested with %{supported}. Backups continue. Look for a newer release of Backup Vault before relying on them.',
      { version: v.version ?? '', supported: v.supported }
    )
  }
}
