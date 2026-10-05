import { beforeEach, describe, expect, it, vi } from 'vitest'
import { job, SPACE_ID } from '../test/fixtures'
import { mountWithHost } from '../test/host'
import RunHistory from './RunHistory.vue'

const link = vi.hoisted(() => ({ to: vi.fn() }))
vi.mock('../composables/useRestoreFolderLink', () => ({
  useRestoreFolderLink: () => link.to
}))

const FOLDER = 'Restore/2026-09-25T03-00-00Z'

function history(runs: Parameters<typeof job>[0][]) {
  return mountWithHost(RunHistory, {
    props: { spaceId: SPACE_ID, runs: runs.map((r) => job(r)) }
  })
}

beforeEach(() => {
  link.to.mockReset()
  link.to.mockReturnValue({ name: 'files-spaces-generic', params: { driveAliasAndItem: 'x' } })
})

describe('RunHistory', () => {
  it('says when nothing has run', () => {
    expect(history([]).text()).toBe('Nothing has run yet.')
  })

  it('shows what a backup processed and who started it', () => {
    const item = history([
      { trigger: 'manual', file_count: 1200, total_bytes: 3_400_000_000 }
    ]).find('tbody tr')
    expect(item.text()).toContain('Backup')
    expect(item.text()).toContain('started by hand')
    expect(item.text()).toContain('1,200 files, 3.4 GB')
  })

  // A clean-up's useful fact is what it removed, not a file count.
  it('shows what a clean-up removed and kept', () => {
    const item = history([{ kind: 'prune', snapshots_deleted: 4, snapshots_kept: 30 }]).find(
      'tbody tr'
    )
    expect(item.text()).toContain('Clean-up of old backups')
    expect(item.text()).toContain('4 removed, 30 kept')
  })

  it('tags the outcome with an icon and words, a failure as danger', () => {
    const [ok, bad] = history([{ id: 'a' }, { id: 'b', state: 'failed' }]).findAll('tbody tr')
    expect(ok!.find('[data-tone]').attributes('data-tone')).toBe('success')
    expect(bad!.find('[data-tone]').attributes('data-tone')).toBe('danger')
    expect(bad!.find('[data-tone]').text()).toBe('Failed')
    expect(bad!.find('[data-icon]').exists()).toBe(true)
  })

  it('shows a failure with its recorded reason and no counts', () => {
    const item = history([
      { state: 'failed', error: 'the backup target is unavailable', file_count: 5 }
    ]).find('tbody tr')
    expect(item.find('[data-testid="run"]').attributes('data-state')).toBe('failed')
    expect(item.text()).toContain('Failed')
    expect(item.text()).toContain('the backup target is unavailable')
    expect(item.text()).not.toContain('files')
  })

  it('names a restore as a restore', () => {
    expect(
      history([{ kind: 'restore' }])
        .find('tbody tr')
        .text()
    ).toMatch(/^Restore/)
  })

  it('links a restore to the folder it wrote into', () => {
    const item = history([{ kind: 'restore', restore_folder: FOLDER }]).find('tbody tr')
    const folder = item.find('[data-testid="history-folder"]')
    expect(folder.text()).toBe(`Restored into: ${FOLDER}`)
    expect(folder.find('[data-testid="folder-link"]').exists()).toBe(true)
    expect(link.to).toHaveBeenCalledWith(SPACE_ID, FOLDER)
  })

  // A failed restore can leave a partial copy behind; the history is where it
  // is found again (8d.3 decision 5).
  it('names the folder of a failed restore as a partial copy', () => {
    const item = history([
      { kind: 'restore', state: 'failed', error: 'the restore run failed', restore_folder: FOLDER }
    ]).find('tbody tr')
    expect(item.find('[data-testid="history-folder"]').text()).toBe(
      `Anything restored before it stopped is in: ${FOLDER}`
    )
  })

  it('names the folder as text when the host cannot link it', () => {
    link.to.mockReturnValue(undefined)
    const folder = history([{ kind: 'restore', restore_folder: FOLDER }]).find(
      '[data-testid="history-folder"]'
    )
    expect(folder.find('[data-testid="folder-link"]').exists()).toBe(false)
    expect(folder.find('[data-testid="folder-path"]').text()).toBe(FOLDER)
  })

  it('shows no folder for a path outside the restore area', () => {
    const item = history([{ kind: 'restore', restore_folder: 'Restore/../Photos' }]).find(
      'tbody tr'
    )
    expect(item.find('[data-testid="history-folder"]').exists()).toBe(false)
    expect(link.to).not.toHaveBeenCalled()
  })

  it('shows no folder for a backup', () => {
    const item = history([{ kind: 'backup', restore_folder: FOLDER }]).find('tbody tr')
    expect(item.find('[data-testid="history-folder"]').exists()).toBe(false)
  })
})
