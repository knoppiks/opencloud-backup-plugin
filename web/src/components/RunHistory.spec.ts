import { describe, expect, it } from 'vitest'
import { job } from '../test/fixtures'
import { mountWithHost } from '../test/host'
import RunHistory from './RunHistory.vue'

function history(runs: Parameters<typeof job>[0][]) {
  return mountWithHost(RunHistory, { props: { runs: runs.map((r) => job(r)) } })
}

describe('RunHistory', () => {
  it('says when nothing has run', () => {
    expect(history([]).text()).toBe('Nothing has run yet.')
  })

  it('shows what a backup processed and who started it', () => {
    const item = history([
      { trigger: 'manual', file_count: 1200, total_bytes: 3_400_000_000 }
    ]).find('li')
    expect(item.text()).toContain('Backup')
    expect(item.text()).toContain('started by hand')
    expect(item.text()).toContain('1,200 files, 3.4 GB')
  })

  // A clean-up's useful fact is what it removed, not a file count.
  it('shows what a clean-up removed and kept', () => {
    const item = history([{ kind: 'prune', snapshots_deleted: 4, snapshots_kept: 30 }]).find('li')
    expect(item.text()).toContain('Clean-up of old backups')
    expect(item.text()).toContain('4 removed, 30 kept')
  })

  it('shows a failure with its recorded reason and no counts', () => {
    const item = history([
      { state: 'failed', error: 'the backup target is unavailable', file_count: 5 }
    ]).find('li')
    expect(item.attributes('data-state')).toBe('failed')
    expect(item.text()).toContain('Failed')
    expect(item.text()).toContain('the backup target is unavailable')
    expect(item.text()).not.toContain('files')
  })

  it('names a restore as a restore', () => {
    expect(
      history([{ kind: 'restore' }])
        .find('li')
        .text()
    ).toMatch(/^Restore/)
  })
})
