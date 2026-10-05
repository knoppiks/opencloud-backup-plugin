// One row of the backup destinations table (Phase 8g): where a destination
// points and how its keys are split. Never the keys themselves: the admin API
// does not return them (decisions.md #14).

import type { AdminTarget } from '../api'
import type { Gettext } from '../api/errortext'

/** TargetRow is what the destinations table shows for one destination. */
export interface TargetRow {
  id: string
  name: string
  /** location is "endpoint / bucket / prefix", as far as it goes. */
  location: string
  keyPairs: string
}

/** targetLocation is "endpoint / bucket / prefix", leaving out what is unset. */
export function targetLocation(target: AdminTarget): string {
  return [target.endpoint, target.bucket, target.prefix].filter(Boolean).join(' / ')
}

/** targetRow builds a row. */
export function targetRow(target: AdminTarget, $gettext: Gettext): TargetRow {
  return {
    id: target.id,
    name: target.name,
    location: targetLocation(target),
    keyPairs: target.maintenance_configured
      ? $gettext('Separate maintenance keys')
      : $gettext('One key pair for everything')
  }
}
