// What to tell a person about a failed request, worded once.
//
// Both the load-failure panel (components/RequestState.vue) and the inline
// failure of an action ("Back up now", saving retention) say the same thing
// about the same code. Two copies would drift, and the drift would be a German
// user reading two different sentences for one problem.
//
// The rule both follow: branch on the code, never on the server's message. The
// message is English, written by the service, and is shown only as a detail.

import type { ApiFailureCode } from './errors'

/** Gettext is the slice of vue3-gettext these helpers need. */
export type Gettext = (msgid: string, parameters?: Record<string, string>) => string

/** errorTitle is the one-line headline for a failure. */
export function errorTitle(code: ApiFailureCode, $gettext: Gettext): string {
  switch (code) {
    case 'offline':
      return $gettext('The backup service cannot be reached')
    case 'timeout':
      return $gettext('The backup service did not answer in time')
    case 'unauthorized':
      return $gettext('Your session has expired')
    case 'forbidden':
      return $gettext('You do not have access to this')
    case 'not_found':
      return $gettext('This is not available')
    case 'not_configured':
      return $gettext('Backups are not set up for this space yet')
    case 'run_in_progress':
      return $gettext('A backup is already running for this space')
    case 'target_unavailable':
      return $gettext('The backup destination cannot be used right now')
    case 'unavailable':
      return $gettext('The backup service is temporarily unavailable')
    case 'upstream_error':
      return $gettext('OpenCloud did not answer the backup service')
    case 'malformed_response':
      return $gettext('The backup service sent something unexpected')
    default:
      return $gettext('Something went wrong')
  }
}

/** errorAdvice says what the person can do about it, or '' when nothing. */
export function errorAdvice(code: ApiFailureCode, $gettext: Gettext): string {
  switch (code) {
    case 'offline':
    case 'timeout':
    case 'unavailable':
      return $gettext('Your backups are unaffected. Try again in a moment.')
    case 'unauthorized':
      return $gettext('Reload the page to sign in again.')
    case 'forbidden':
      return $gettext('Ask a manager of this space if you need access.')
    case 'upstream_error':
      return $gettext('This usually clears up on its own. Try again in a moment.')
    default:
      return ''
  }
}
