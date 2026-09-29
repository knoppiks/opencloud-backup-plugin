// Whether the signed-in user is an OpenCloud administrator — for deciding what
// to *offer*, never what is allowed. The backend's admin middleware decides
// that, and a 403 from it wins over anything this says (8e decision 1).
//
// `read-all` on `Setting` is the rule web-pkg itself uses for admin-only UI,
// and upstream admin-settings too. Measured on the fixture: true for `admin`,
// false for a normal user.

import { useAbility } from '@opencloud-eu/web-pkg'

/** useIsAdmin reports whether the caller may be offered the admin view. */
export function useIsAdmin(): boolean {
  return useAbility().can('read-all', 'Setting')
}
