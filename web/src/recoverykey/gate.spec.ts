import { describe, expect, it } from 'vitest'
import { generateRecoveryKey } from '../crypto'
import { gateHint, gateMatches, pickGateGroups } from './gate'

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

  it('counts groups after the prefix, not the saved key’s dash-separated parts', () => {
    const key = generateRecoveryKey().display
    const parts = key.split('-')
    // Group index 1 ("Group 2 after ocbk1-") is the saved key's third part.
    expect(gateMatches(key, [1, 4], [parts[2]!, parts[5]!])).toBe(true)
    expect(gateMatches(key, [1, 4], [parts[1]!, parts[4]!])).toBe(false)
  })

  it('draws the key’s shape with only the asked group marked', () => {
    const hint = gateHint(6)
    expect(hint.map((p) => p.text).join('-')).toBe('ocbk1-•••••-•••••-•••••-•••••-•••••-•••••-____')
    expect(hint.filter((p) => p.asked)).toEqual([{ text: '____', asked: true }])
    for (let group = 0; group < 7; group++) {
      const parts = gateHint(group)
      expect(parts).toHaveLength(8)
      expect(parts.findIndex((p) => p.asked)).toBe(group + 1)
      // Same length as a real key, so the drawing lines up with the saved copy.
      expect(parts.map((p) => p.text).join('-')).toHaveLength(generateRecoveryKey().display.length)
    }
  })
})
