// What the overview and the board offer a Space that is not fully set up.
//
// One rule, shared by the card and the board, built on the wizard's own resume
// rule so the button and the wizard it opens can never disagree about what is
// left to do.

import type { BackupStatus } from '../api'
import type { Gettext } from '../api/errortext'
import { canManageKeys, canOperate } from './roles'
import { nextStep } from './setupstep'

/**
 * SetupAction is the one entry point a Space offers.
 *
 * `needs_manager` is not a link: an editor has done what an editor can, and a
 * button that opens a page saying so would be a button that does nothing.
 */
export type SetupAction = 'start' | 'finish' | 'turn_on' | 'needs_manager'

/** setupAction decides the entry point, or undefined when there is none. */
export function setupAction(status: BackupStatus, role: string): SetupAction | undefined {
  if (!canOperate(role)) {
    return undefined
  }
  switch (nextStep(status)) {
    case 'target':
      return status.keys_configured ? 'finish' : 'start'
    case 'keys':
      return canManageKeys(role) ? 'finish' : 'needs_manager'
    case 'schedule':
      return 'turn_on'
    case 'done':
      return undefined
  }
}

/** setupActionLabel is the link text, or the sentence for needs_manager. */
export function setupActionLabel(action: SetupAction, $gettext: Gettext): string {
  switch (action) {
    case 'start':
      return $gettext('Set up backup')
    case 'finish':
      return $gettext('Finish setup')
    case 'turn_on':
      return $gettext('Turn on scheduled backups')
    case 'needs_manager':
      return $gettext('A manager of this space has to finish setup.')
  }
}
