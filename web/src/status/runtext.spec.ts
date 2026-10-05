import { describe, expect, it } from 'vitest'
import { job } from '../test/fixtures'
import { runDetails, runLook, runStateLabel } from './runtext'

const t = (msgid: string, params?: Record<string, string>) =>
  Object.entries(params ?? {}).reduce((s, [k, v]) => s.replace(`%{${k}}`, v), msgid)
const format = { count: (n: number) => String(n), bytes: (n: number) => `${n} B` }

describe('runtext', () => {
  it('tags only a failed run as danger, each with an icon', () => {
    expect(runLook(job({ state: 'succeeded' }))).toEqual({
      tone: 'success',
      icon: 'checkbox-circle'
    })
    expect(runLook(job({ state: 'failed' }))).toEqual({ tone: 'danger', icon: 'close-circle' })
    expect(runLook(job({ state: 'running' })).tone).toBe('info')
    expect(runLook(job({ state: 'pending' })).tone).toBe('neutral')
  })

  it('passes an unknown state through as its own word', () => {
    expect(runStateLabel(job({ state: 'mystery' as never }), t)).toBe('mystery')
  })

  it('counts only what a succeeded run did', () => {
    expect(runDetails(job({ file_count: 3, total_bytes: 9 }), t, format)).toBe('3 files, 9 B')
    expect(runDetails(job({ state: 'failed', file_count: 3 }), t, format)).toBe('')
    expect(
      runDetails(job({ kind: 'prune', snapshots_deleted: 1, snapshots_kept: 2 }), t, format)
    ).toBe('1 removed, 2 kept')
  })
})
