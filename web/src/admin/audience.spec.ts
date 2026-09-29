import { describe, expect, it } from 'vitest'
import type { Grant } from '../api'
import {
  audienceFromGrants,
  grantsFromAudience,
  hasRedundantPeople,
  reachesNobody,
  sameGrants,
  withMode,
  withUser,
  withoutUser
} from './audience'

const SPACE_GRANT: Grant = { scope: 'space', space_id: 'storage$space!opaque' }

describe('audienceFromGrants', () => {
  it('reads an all_users grant as everyone', () => {
    expect(audienceFromGrants([{ scope: 'all_users' }])).toEqual({
      mode: 'everyone',
      userIds: [],
      kept: []
    })
  })

  it('reads user grants as the listed people, once each', () => {
    const audience = audienceFromGrants([
      { scope: 'user', user_id: 'u-2' },
      { scope: 'user', user_id: 'u-1' },
      { scope: 'user', user_id: 'u-2' }
    ])
    expect(audience).toEqual({ mode: 'people', userIds: ['u-2', 'u-1'], kept: [] })
  })

  it('keeps space grants and anything it does not understand, verbatim', () => {
    const future = { scope: 'group', group_id: 'g' } as unknown as Grant
    const audience = audienceFromGrants([SPACE_GRANT, future])
    expect(audience.kept).toEqual([SPACE_GRANT, future])
  })

  it('reads an empty list as people, with nobody listed', () => {
    const audience = audienceFromGrants([])
    expect(audience).toEqual({ mode: 'people', userIds: [], kept: [] })
    expect(reachesNobody(audience)).toBe(true)
  })
})

describe('grantsFromAudience', () => {
  // PUT replaces the whole list: a space grant left out would be revoked.
  it('sends kept grants back on every save', () => {
    const audience = audienceFromGrants([SPACE_GRANT, { scope: 'user', user_id: 'u-1' }])
    expect(grantsFromAudience(audience)).toEqual([{ scope: 'user', user_id: 'u-1' }, SPACE_GRANT])
    expect(grantsFromAudience(withMode(audience, 'everyone'))).toEqual([
      { scope: 'all_users' },
      SPACE_GRANT
    ])
  })

  it('drops listed people when everyone is chosen, and says so first', () => {
    const audience = audienceFromGrants([{ scope: 'all_users' }, { scope: 'user', user_id: 'u' }])
    expect(hasRedundantPeople(audience)).toBe(true)
    expect(grantsFromAudience(audience)).toEqual([{ scope: 'all_users' }])
  })

  it('sends an empty list for nobody', () => {
    expect(grantsFromAudience(audienceFromGrants([]))).toEqual([])
  })
})

describe('editing', () => {
  it('adds a person once and removes them', () => {
    const start = audienceFromGrants([])
    const added = withUser(withUser(start, 'u-1'), 'u-1')
    expect(added.userIds).toEqual(['u-1'])
    expect(withoutUser(added, 'u-1').userIds).toEqual([])
    // Pure: the input is not changed.
    expect(start.userIds).toEqual([])
  })

  it('does not count a space grant as nobody', () => {
    expect(reachesNobody(audienceFromGrants([SPACE_GRANT]))).toBe(false)
  })

  it('compares by what would be stored, not by order', () => {
    const a = audienceFromGrants([
      { scope: 'user', user_id: 'u-1' },
      { scope: 'user', user_id: 'u-2' }
    ])
    const b = audienceFromGrants([
      { scope: 'user', user_id: 'u-2' },
      { scope: 'user', user_id: 'u-1' }
    ])
    expect(sameGrants(a, b)).toBe(true)
    expect(sameGrants(a, withoutUser(a, 'u-1'))).toBe(false)
    // Switching to everyone and back changes nothing that would be stored.
    expect(sameGrants(a, withMode(withMode(a, 'everyone'), 'people'))).toBe(true)
  })
})
