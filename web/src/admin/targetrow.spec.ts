import { describe, expect, it } from 'vitest'
import { adminTarget, TARGET_ID } from '../test/fixtures'
import { targetLocation, targetRow } from './targetrow'

const t = (msgid: string) => msgid

describe('targetLocation', () => {
  it('joins endpoint, bucket and prefix', () => {
    expect(targetLocation(adminTarget({ prefix: 'family/' }))).toBe(
      'buddy.example.org:3900 / backups / family/'
    )
  })

  it('leaves out an unset prefix', () => {
    expect(targetLocation(adminTarget())).toBe('buddy.example.org:3900 / backups')
  })
})

describe('targetRow', () => {
  it('says how the keys are split, never what they are', () => {
    expect(targetRow(adminTarget(), t)).toEqual({
      id: TARGET_ID,
      name: 'Buddy',
      location: 'buddy.example.org:3900 / backups',
      keyPairs: 'One key pair for everything'
    })
    expect(targetRow(adminTarget({ maintenance_configured: true }), t).keyPairs).toBe(
      'Separate maintenance keys'
    )
  })
})
