// One row of the Spaces table (Phase 8g): what the overview shows for a
// Space, worked out from its status without rendering anything.
//
// Each Space's status loads on its own, so a row can be still loading, have
// failed, or be ready; only a ready row has a state and dates.

import type { ApiError, BackupStatus, Space } from '../api'
import type { Gettext } from '../api/errortext'
import { setupAction, type SetupAction } from './setupaction'
import { needsAttention, spaceState, type SpaceState } from './spacestate'

/** SpaceResult is a Space's status, or why it has none, or neither yet. */
export interface SpaceResult {
  status?: BackupStatus
  error?: ApiError
}

/** OverviewRow is what the table shows for one Space. */
export interface OverviewRow {
  id: string
  name: string
  /** personal is the signed-in user's own Space. */
  personal: boolean
  kind: string
  state?: SpaceState | undefined
  error?: ApiError | undefined
  /** lastBackup and nextBackup are display text; empty while loading. */
  lastBackup: string
  nextBackup: string
  /** action is the setup entry point the row offers, if any. */
  action?: SetupAction | undefined
}

/** NOT_APPLICABLE fills a date cell that has no meaning for the Space. */
export const NOT_APPLICABLE = '—'

/**
 * overviewRow builds a row. `when` formats a timestamp for people.
 *
 * "A manager has to finish setup" is not offered as a row action: it is a
 * sentence, not a link, and the Space's own page says it.
 */
export function overviewRow(
  space: Space,
  result: SpaceResult | undefined,
  $gettext: Gettext,
  when: (iso: string) => string
): OverviewRow {
  const row: OverviewRow = {
    id: space.id,
    name: space.name,
    personal: space.type === 'personal',
    kind: space.type === 'personal' ? $gettext('Personal space') : $gettext('Shared space'),
    lastBackup: '',
    nextBackup: ''
  }
  const status = result?.status
  if (!status) {
    return { ...row, error: result?.error }
  }
  const state = spaceState(status)
  const action = setupAction(status, space.role)
  return {
    ...row,
    state,
    action: action === 'needs_manager' ? undefined : action,
    lastBackup: lastBackupText(state, status, $gettext, when),
    nextBackup: nextBackupText(state, status, $gettext, when)
  }
}

function isSetUp(state: SpaceState): boolean {
  return state !== 'not_set_up' && state !== 'setup_incomplete'
}

function lastBackupText(
  state: SpaceState,
  status: BackupStatus,
  $gettext: Gettext,
  when: (iso: string) => string
): string {
  if (!isSetUp(state)) {
    return NOT_APPLICABLE
  }
  return status.last_successful_run
    ? when(status.last_successful_run.created_at)
    : $gettext('None yet')
}

function nextBackupText(
  state: SpaceState,
  status: BackupStatus,
  $gettext: Gettext,
  when: (iso: string) => string
): string {
  if (!isSetUp(state)) {
    return NOT_APPLICABLE
  }
  if (!status.enabled) {
    return $gettext('Off')
  }
  return status.next_run ? when(status.next_run) : $gettext('Not scheduled')
}

/** OverviewSummary counts the rows for the table's footer. */
export interface OverviewSummary {
  total: number
  protected: number
  attention: number
}

/** overviewSummary counts protected Spaces and those that need attention. */
export function overviewSummary(rows: OverviewRow[]): OverviewSummary {
  return {
    total: rows.length,
    protected: rows.filter((r) => r.state === 'active').length,
    attention: rows.filter((r) => r.state !== undefined && needsAttention(r.state)).length
  }
}

/** compareSpaces puts the personal Space first, then the others by name. */
export function compareSpaces(a: Space, b: Space): number {
  const personal = Number(b.type === 'personal') - Number(a.type === 'personal')
  return personal !== 0 ? personal : a.name.localeCompare(b.name)
}
