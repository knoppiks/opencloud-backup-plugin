// What the admin view says about failures and check results.
//
// The shared wording (api/errortext.ts) is written for Space members: its 403
// says "ask a manager of this space". Here a 403 means the caller is not an
// administrator, and the server's answer is the one that counts — the client
// gate only decides what is offered (8e decision 1).

import type { ApiFailureCode, CheckResult } from '../api'
import { errorAdvice, errorTitle, type Gettext } from '../api/errortext'
import type { Tone } from '../layout/tone'

/** adminErrorTitle is errorTitle as the admin view says it. */
export function adminErrorTitle(code: ApiFailureCode, $gettext: Gettext): string {
  switch (code) {
    case 'forbidden':
      return $gettext('Only administrators can manage backup destinations')
    case 'not_found':
      return $gettext('This backup destination does not exist')
    case 'bad_request':
      return $gettext('The backup service did not accept this')
    case 'target_in_use':
      return $gettext('This backup destination is still in use')
    default:
      return errorTitle(code, $gettext)
  }
}

/** adminErrorAdvice is errorAdvice as the admin view says it. */
export function adminErrorAdvice(code: ApiFailureCode, $gettext: Gettext): string {
  switch (code) {
    case 'forbidden':
      return $gettext('Sign in with an administrator account to continue.')
    case 'not_found':
      return $gettext('It may have been deleted in the meantime.')
    case 'bad_request':
      return $gettext('The reason is below.')
    case 'unavailable':
      // Two different causes answer 503 here (no target-wrap key; checks not
      // available), and neither clears up by waiting. The server's detail,
      // shown underneath, says which.
      return $gettext('The reason is below. Ask whoever runs the backup service.')
    default:
      return errorAdvice(code, $gettext)
  }
}

/**
 * spacesInUse reads the count from a `target_in_use` message
 * ("2 space(s) still back up to this target; …"). The server sends a count
 * and never an id (decisions.md #15), and the message is its only carrier.
 */
export function spacesInUse(serverMessage: string | undefined): number | undefined {
  const match = /^\s*(\d+)\s/.exec(serverMessage ?? '')
  return match ? Number(match[1]) : undefined
}

/** checkRoleText names the key pair a check result is about. */
export function checkRoleText(role: string, $gettext: Gettext): string {
  switch (role) {
    case 'backup':
      return $gettext('Backup keys')
    case 'maintenance':
      return $gettext('Maintenance keys')
    default:
      return role
  }
}

/**
 * checkTone is how a connection check's results are drawn: success only when
 * every key pair works, danger as soon as one does not.
 */
export function checkTone(results: Pick<CheckResult, 'outcome'>[]): Tone {
  return results.every((r) => r.outcome === 'ok') ? 'success' : 'danger'
}

/** checkOutcomeText is the one word (or few) a check result says. */
export function checkOutcomeText(outcome: string, $gettext: Gettext): string {
  switch (outcome) {
    case 'ok':
      return $gettext('Works')
    case 'unreachable':
      return $gettext('Endpoint not reachable')
    case 'timeout':
      return $gettext('No answer in time')
    case 'auth_failed':
      return $gettext('Keys not accepted')
    case 'denied':
      return $gettext('Access denied')
    case 'bucket_missing':
      return $gettext('Bucket not found')
    default:
      return $gettext('Unknown problem')
  }
}
