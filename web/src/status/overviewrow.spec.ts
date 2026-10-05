import { describe, expect, it } from 'vitest'
import { ApiError } from '../api'
import { job, space, status } from '../test/fixtures'
import {
  NOT_APPLICABLE,
  compareSpaces,
  overviewRow,
  overviewSummary,
  type OverviewRow
} from './overviewrow'

const t = (msgid: string) => msgid
const when = (iso: string) => `when(${iso})`
/** omit drops optional fields, which exactOptionalPropertyTypes will not set to undefined. */
function omit<T extends object, K extends keyof T>(value: T, ...keys: K[]): Omit<T, K> {
  const copy = { ...value }
  for (const key of keys) {
    delete copy[key]
  }
  return copy
}
const unset = { configured: false, keys_configured: false, enabled: false }

describe('overviewRow', () => {
  it('names a healthy Space protected, with its last and next backup', () => {
    const row = overviewRow(space(), { status: status() }, t, when)
    expect(row.state).toBe('active')
    expect(row.lastBackup).toBe(`when(${job().created_at})`)
    expect(row.nextBackup).toBe('when(2026-09-25T00:30:00Z)')
    expect(row.action).toBeUndefined()
  })

  it('distinguishes personal and shared Spaces', () => {
    expect(overviewRow(space({ type: 'personal' }), undefined, t, when)).toMatchObject({
      personal: true,
      kind: 'Personal space'
    })
    expect(overviewRow(space({ type: 'project' }), undefined, t, when)).toMatchObject({
      personal: false,
      kind: 'Shared space'
    })
  })

  it('is still loading without a result', () => {
    const row = overviewRow(space(), undefined, t, when)
    expect(row.state).toBeUndefined()
    expect(row.error).toBeUndefined()
    expect(row.lastBackup).toBe('')
  })

  it('carries its own error instead of a state', () => {
    const error = new ApiError('forbidden', 'x', 403)
    const row = overviewRow(space(), { error }, t, when)
    expect(row.state).toBeUndefined()
    expect(row.error).toBe(error)
  })

  it('has no dates for a Space nobody set up, and offers setup to an editor', () => {
    const row = overviewRow(space(), { status: status(unset) }, t, when)
    expect(row.state).toBe('not_set_up')
    expect(row.lastBackup).toBe(NOT_APPLICABLE)
    expect(row.nextBackup).toBe(NOT_APPLICABLE)
    expect(row.action).toBe('start')
  })

  it('offers a viewer nothing', () => {
    const row = overviewRow(space({ role: 'viewer' }), { status: status(unset) }, t, when)
    expect(row.action).toBeUndefined()
  })

  // "A manager has to finish" is a sentence, not a link: no dead-end button.
  it('offers an editor nothing when only a manager can continue', () => {
    const row = overviewRow(
      space({ role: 'editor' }),
      { status: status({ configured: true, keys_configured: false }) },
      t,
      when
    )
    expect(row.action).toBeUndefined()
  })

  it('says when no backup has succeeded yet, and when the schedule is off', () => {
    const row = overviewRow(
      space(),
      { status: omit(status({ enabled: false }), 'last_successful_run', 'last_run') },
      t,
      when
    )
    expect(row.lastBackup).toBe('None yet')
    expect(row.nextBackup).toBe('Off')
  })

  it('says when nothing is scheduled', () => {
    const row = overviewRow(space(), { status: omit(status(), 'next_run') }, t, when)
    expect(row.nextBackup).toBe('Not scheduled')
  })

  it('keeps the last good backup on a failed Space', () => {
    const row = overviewRow(
      space(),
      { status: status({ last_run: job({ id: 'j2', state: 'failed' }) }) },
      t,
      when
    )
    expect(row.state).toBe('failed')
    expect(row.lastBackup).toBe(`when(${job().created_at})`)
  })
})

describe('overviewSummary', () => {
  it('counts protected Spaces and those that need attention, not loading ones', () => {
    const rows = [
      { state: 'active' },
      { state: 'failed' },
      { state: 'stale' },
      { state: 'not_set_up' },
      {}
    ] as OverviewRow[]
    expect(overviewSummary(rows)).toEqual({ total: 5, protected: 1, attention: 2 })
  })
})

describe('compareSpaces', () => {
  it('puts the personal Space first, then the others by name', () => {
    const sorted = [
      space({ name: 'Zoo' }),
      space({ name: 'Attic' }),
      space({ name: 'Me', type: 'personal' })
    ].sort(compareSpaces)
    expect(sorted.map((s) => s.name)).toEqual(['Me', 'Attic', 'Zoo'])
  })
})
