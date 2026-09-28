import { describe, expect, it } from 'vitest'
import type { BackupStatus } from '../api'
import { status } from '../test/fixtures'
import { setupAction, setupActionLabel, type SetupAction } from './setupaction'

const flags = (configured: boolean, keys: boolean, enabled: boolean): Partial<BackupStatus> => ({
  configured,
  keys_configured: keys,
  enabled
})

describe('setupAction', () => {
  it.each<[string, Partial<BackupStatus>, string, SetupAction | undefined]>([
    ['nothing yet, editor', flags(false, false, false), 'editor', 'start'],
    ['nothing yet, manager', flags(false, false, false), 'manager', 'start'],
    ['keys but no target', flags(false, true, false), 'editor', 'finish'],
    ['target, no keys, manager', flags(true, false, false), 'manager', 'finish'],
    ['target, no keys, owner', flags(true, false, false), 'owner', 'finish'],
    ['target, no keys, editor', flags(true, false, false), 'editor', 'needs_manager'],
    ['keys, runs off', flags(true, true, false), 'editor', 'turn_on'],
    ['complete', flags(true, true, true), 'manager', undefined]
  ])('%s', (_, overrides, role, want) => {
    expect(setupAction(status(overrides), role)).toBe(want)
  })

  it.each(['viewer', 'something-new'])('offers %s nothing, whatever the state', (role) => {
    for (const s of [
      flags(false, false, false),
      flags(true, false, false),
      flags(true, true, false)
    ]) {
      expect(setupAction(status(s), role)).toBeUndefined()
    }
  })

  it('has words for every action', () => {
    const actions: SetupAction[] = ['start', 'finish', 'turn_on', 'needs_manager']
    const labels = actions.map((a) => setupActionLabel(a, (id) => id))
    expect(new Set(labels).size).toBe(actions.length)
  })
})
