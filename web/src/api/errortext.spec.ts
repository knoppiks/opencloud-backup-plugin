import { describe, expect, it } from 'vitest'
import { errorAdvice, errorTitle } from './errortext'

const $gettext = (msgid: string) => msgid

describe('error wording', () => {
  // A body over the service's limit is refused before anything is done; the
  // person should hear that, not "something went wrong" (#90).
  it('names a too-large request and says nothing was changed', () => {
    expect(errorTitle('payload_too_large', $gettext)).toBe(
      'This request is too large for the backup service'
    )
    expect(errorAdvice('payload_too_large', $gettext)).toContain('Nothing was changed')
  })

  it('does not suggest that waiting fixes a too-large request', () => {
    expect(errorAdvice('payload_too_large', $gettext)).not.toContain('moment')
  })
})
