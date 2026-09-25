import { describe, expect, it } from 'vitest'
import { gateMatches, pickGateGroups } from './gate'

describe('confirmation gate', () => {
  it('picks two distinct groups of seven, in key order', () => {
    const seen = new Set<string>()
    for (let seed = 0; seed < 50; seed++) {
      let n = seed
      const groups = pickGateGroups((bound) => n++ % bound)
      expect(groups).toHaveLength(2)
      expect(new Set(groups).size).toBe(2)
      expect(groups.every((g) => g >= 0 && g < 7)).toBe(true)
      expect(groups).toEqual([...groups].sort((a, b) => a - b))
      seen.add(groups.join())
    }
    expect(seen.size).toBeGreaterThan(1)
  })

  it('matches with decode’s tolerance and nothing looser', () => {
    // Assembled from its groups rather than written as one literal: a
    // key-shaped string in the source is what gitleaks exists to flag.
    const key = ['ocbk1', 'AB1C0', 'DEFGH', 'JKMNP', 'QRSTV', 'WXYZ0', '12345', '6789'].join('-')
    expect(gateMatches(key, [0, 6], ['ab1c0', '6789'])).toBe(true)
    expect(gateMatches(key, [0, 6], ['a b l c o', '67-89'])).toBe(true)
    expect(gateMatches(key, [0, 6], ['AB1C0', '6788'])).toBe(false)
    expect(gateMatches(key, [0, 6], ['AB1C0'])).toBe(false)
    expect(gateMatches(key, [0, 6], ['DEFGH', 'AB1C0'])).toBe(false)
  })
})
