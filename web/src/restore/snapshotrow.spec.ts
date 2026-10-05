import { describe, expect, it } from 'vitest'
import { snapshot } from '../test/fixtures'
import { snapshotRow } from './snapshotrow'

const format = {
  when: (iso: string) => `when(${iso})`,
  count: (n: number) => `count(${n})`,
  bytes: (n: number) => `bytes(${n})`
}

describe('snapshotRow', () => {
  it('shows a backup as date, files and size', () => {
    expect(snapshotRow(snapshot(), format)).toEqual({
      id: 'k0123456789abcdef',
      when: 'when(2026-09-23T01:30:00Z)',
      files: 'count(1200)',
      size: 'bytes(3400000000)'
    })
  })

  it('counts a backup with no statistics as empty, not as missing', () => {
    const bare = snapshot({
      file_count: undefined as unknown as number,
      total_bytes: undefined as unknown as number
    })
    expect(snapshotRow(bare, format)).toMatchObject({ files: 'count(0)', size: 'bytes(0)' })
  })
})
