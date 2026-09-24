import { describe, expect, it } from 'vitest'
import type { BackupStatus, Job } from '../api'
import { canManageKeys, canOperate } from './roles'
import { needsAttention, spaceState, type SpaceState } from './spacestate'
import { stateAdvice, stateLabel } from './statetext'

function job(state: Job['state'], kind: Job['kind'] = 'backup'): Job {
  return { id: 'j', kind, state, created_at: '2026-09-24T01:00:00Z', updated_at: '' }
}

/** healthy is a set-up, enabled Space whose last backup succeeded. */
function healthy(
  overrides: Partial<BackupStatus> = {},
  omit: (keyof BackupStatus)[] = []
): BackupStatus {
  const status: BackupStatus = {
    space_id: 's',
    configured: true,
    keys_configured: true,
    stale: false,
    enabled: true,
    running: false,
    last_run: job('succeeded'),
    last_successful_run: job('succeeded'),
    ...overrides
  }
  for (const key of omit) {
    delete status[key]
  }
  return status
}

describe('spaceState', () => {
  it.each<[string, Partial<BackupStatus>, SpaceState]>([
    ['nothing configured', { configured: false, keys_configured: false }, 'not_set_up'],
    ['target without keys', { keys_configured: false }, 'setup_incomplete'],
    ['keys without target', { configured: false }, 'setup_incomplete'],
    ['a run in flight', { running: true, current_job: job('running') }, 'running'],
    ['last backup failed', { last_run: job('failed') }, 'failed'],
    ['stale', { stale: true }, 'stale'],
    ['schedule off', { enabled: false }, 'paused'],
    ['healthy', {}, 'active']
  ])('%s', (_name, overrides, want) => {
    expect(spaceState(healthy(overrides))).toBe(want)
  })

  it('never ran', () => {
    expect(spaceState(healthy({}, ['last_run', 'last_successful_run']))).toBe('waiting')
  })

  // Precedence: what is happening now beats what went wrong before, and a
  // failure is more specific than "stale", which it usually causes.
  it('orders overlapping states by precedence', () => {
    expect(spaceState(healthy({ running: true, last_run: job('failed'), stale: true }))).toBe(
      'running'
    )
    expect(spaceState(healthy({ last_run: job('failed'), stale: true }))).toBe('failed')
    expect(spaceState(healthy({ stale: true, enabled: false }))).toBe('stale')
    // An unfinished setup outranks everything: no other state is meaningful.
    expect(spaceState(healthy({ keys_configured: false, running: true }))).toBe('setup_incomplete')
  })

  it('does not compute staleness itself', () => {
    // A success from long ago is still "active" unless the server says stale:
    // the rule lives server-side, with the notification it must agree with.
    const ancient = { ...job('succeeded'), created_at: '2000-01-01T00:00:00Z' }
    expect(spaceState(healthy({ last_run: ancient, last_successful_run: ancient }))).toBe('active')
  })

  it('marks the states a member should act on', () => {
    expect(needsAttention('failed')).toBe(true)
    expect(needsAttention('stale')).toBe(true)
    expect(needsAttention('setup_incomplete')).toBe(true)
    expect(needsAttention('active')).toBe(false)
    expect(needsAttention('not_set_up')).toBe(false)
  })
})

describe('roles', () => {
  it.each([
    ['viewer', false, false],
    ['editor', true, false],
    ['manager', true, true],
    ['owner', true, true]
  ])('%s', (role, operate, keys) => {
    expect(canOperate(role)).toBe(operate)
    expect(canManageKeys(role)).toBe(keys)
  })

  // Offering too little is a missing button; offering too much is a button
  // that can only fail.
  it('treats an unknown role as a viewer', () => {
    expect(canOperate('superuser')).toBe(false)
    expect(canManageKeys('')).toBe(false)
  })
})

describe('state text', () => {
  const states: SpaceState[] = [
    'not_set_up',
    'setup_incomplete',
    'running',
    'failed',
    'stale',
    'paused',
    'waiting',
    'active'
  ]
  const id = (msgid: string) => msgid

  it('labels every state', () => {
    const labels = states.map((s) => stateLabel(s, id))
    expect(labels.every((l) => l.length > 0)).toBe(true)
    expect(new Set(labels).size).toBe(states.length)
  })

  it('explains every state that needs explaining', () => {
    for (const s of states) {
      expect(typeof stateAdvice(s, id)).toBe('string')
    }
    expect(stateAdvice('failed', id)).toMatch(/still safe/)
  })
})
