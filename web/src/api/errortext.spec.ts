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

  // The bundle declares every body as JSON, so a 415 means it and the service
  // disagree; reloading fetches the matching bundle (#92).
  it('names a refused request, says nothing was changed and suggests a reload', () => {
    expect(errorTitle('unsupported_media_type', $gettext)).toBe(
      'The backup service did not accept this request'
    )
    expect(errorAdvice('unsupported_media_type', $gettext)).toContain('Nothing was changed')
    expect(errorAdvice('unsupported_media_type', $gettext)).toContain('Reload the page')
  })
})
