// Whether this bundle and the service it talks to belong together
// (decision #24, compatibility-policy.md §1).
//
// The two must run at the same version. How loud a difference is depends on
// what it means:
//
// - a different API level: requests may fail or be misread. Admins are warned
//   and told what to do; members get a calm notice, because reloading picks
//   up a bundle that was updated meanwhile, and otherwise it is the
//   administrator's to fix.
// - the same API level, a different version: nothing should break, but it is
//   not a supported combination. Admins are told; members are not, there is
//   nothing for them to do.
// - either side "dev" (an unstamped build): only the API level is compared,
//   so development and the E2E fixture stay quiet.

import type { ServiceVersion } from '../api'
import type { Gettext } from '../api/errortext'
import type { Tone } from '../layout/tone'
import { DEV_VERSION } from './appversion'

/** BundleVersion is what this bundle knows about itself. */
export interface BundleVersion {
  version: string
  apiLevel: number
}

/** Mismatch is how the bundle and the service differ, if at all. */
export type Mismatch = 'none' | 'api_level' | 'version'

export function compareWithService(service: ServiceVersion, bundle: BundleVersion): Mismatch {
  if (service.api_level !== bundle.apiLevel) {
    return 'api_level'
  }
  if (service.version === DEV_VERSION || bundle.version === DEV_VERSION) {
    return 'none'
  }
  return service.version === bundle.version ? 'none' : 'version'
}

/** VersionNotice is what to show: a tone, a title and one sentence. */
export interface VersionNotice {
  tone: Tone
  title: string
  message: string
}

/**
 * versionNotice is the notice for the caller's audience, or undefined when
 * there is nothing to say: the service did not answer, the two match, or the
 * difference is not the caller's business.
 */
export function versionNotice(
  service: ServiceVersion | undefined,
  bundle: BundleVersion,
  isAdmin: boolean,
  $gettext: Gettext
): VersionNotice | undefined {
  if (!service) {
    return undefined
  }
  const params = { bundle: bundle.version, service: service.version }
  switch (compareWithService(service, bundle)) {
    case 'none':
      return undefined
    case 'api_level':
      return isAdmin
        ? {
            tone: 'warning',
            title: $gettext('Backup Vault and its service do not match'),
            message: $gettext(
              'This page is Backup Vault %{bundle}; the backup service is %{service}. Some actions may fail. Run the web extension and the service at the same version, then reload the page.',
              params
            )
          }
        : {
            tone: 'info',
            title: '',
            message: $gettext(
              'Backup Vault is being updated. If something does not work, reload the page; if it still does not, ask your administrator.'
            )
          }
    case 'version':
      return isAdmin
        ? {
            tone: 'info',
            title: '',
            message: $gettext(
              'This page is Backup Vault %{bundle}; the backup service is %{service}. They work together, but run them at the same version.',
              params
            )
          }
        : undefined
  }
}
