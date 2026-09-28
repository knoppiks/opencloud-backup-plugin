// The words for each SpaceState, shared by the card and the status board.

import type { Gettext } from '../api/errortext'
import type { SpaceState } from './spacestate'

/** stateLabel is the short badge text for a state. */
export function stateLabel(state: SpaceState, $gettext: Gettext): string {
  switch (state) {
    case 'not_set_up':
      return $gettext('Not backed up')
    case 'setup_incomplete':
      return $gettext('Setup not finished')
    case 'running':
      return $gettext('Running')
    case 'failed':
      return $gettext('Last backup failed')
    case 'stale':
      return $gettext('Backups have stopped')
    case 'paused':
      return $gettext('Scheduled backups off')
    case 'waiting':
      return $gettext('Waiting for first backup')
    case 'active':
      return $gettext('Protected')
  }
}

/**
 * stateAdvice is the sentence under the badge: what the state means for the
 * family's data, in words that do not assume they know what a snapshot is.
 */
export function stateAdvice(state: SpaceState, $gettext: Gettext): string {
  switch (state) {
    case 'not_set_up':
      return $gettext('Nothing in this space is being backed up.')
    case 'setup_incomplete':
      return $gettext('Backup setup was started but not finished, so nothing is backed up yet.')
    case 'running':
      return ''
    case 'failed':
      return $gettext('Earlier backups are still safe. The next scheduled run will try again.')
    case 'stale':
      return $gettext('Backups have not succeeded for longer than the schedule allows.')
    case 'paused':
      return $gettext('Existing backups are kept, but no new ones are made unless started by hand.')
    case 'waiting':
      return $gettext('The first backup will run at the next scheduled time.')
    case 'active':
      return ''
  }
}
