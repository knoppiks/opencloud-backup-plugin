import { describe, expect, it } from 'vitest'
import { errorTitle } from '../api/errortext'
import {
  adminErrorAdvice,
  adminErrorTitle,
  checkOutcomeText,
  checkRoleText,
  spacesInUse
} from './wording'

const $gettext = (msgid: string) => msgid

describe('admin error wording', () => {
  // The shared 403 wording sends people to a Space manager. Here it would be
  // wrong: the only fix is an administrator account.
  it('reads a 403 as "not an administrator", not as a Space permission', () => {
    expect(adminErrorTitle('forbidden', $gettext)).toBe(
      'Only administrators can manage backup destinations'
    )
    expect(adminErrorAdvice('forbidden', $gettext)).not.toContain('manager')
  })

  it('falls back to the shared wording for everything else', () => {
    expect(adminErrorTitle('offline', $gettext)).toBe(errorTitle('offline', $gettext))
  })

  it('does not promise that waiting fixes a 503', () => {
    expect(adminErrorAdvice('unavailable', $gettext)).not.toContain('moment')
  })
})

describe('spacesInUse', () => {
  it('reads the count the server sent', () => {
    expect(spacesInUse('3 space(s) still back up to this target; point them elsewhere first')).toBe(
      3
    )
  })

  it('answers undefined when there is no count to read', () => {
    expect(spacesInUse(undefined)).toBeUndefined()
    expect(spacesInUse('in use')).toBeUndefined()
  })
})

describe('check wording', () => {
  it.each(['ok', 'unreachable', 'timeout', 'auth_failed', 'denied', 'bucket_missing'])(
    'has its own words for %s',
    (outcome) => {
      expect(checkOutcomeText(outcome, $gettext)).not.toBe(checkOutcomeText('unknown', $gettext))
    }
  )

  it('reads an outcome it does not know as unknown', () => {
    expect(checkOutcomeText('new_thing', $gettext)).toBe('Unknown problem')
  })

  it('names both roles, and shows an unknown role as sent', () => {
    expect(checkRoleText('backup', $gettext)).toBe('Backup keys')
    expect(checkRoleText('maintenance', $gettext)).toBe('Maintenance keys')
    expect(checkRoleText('other', $gettext)).toBe('other')
  })
})
