import { describe, expect, it, vi } from 'vitest'
import { randomBytes } from '../crypto'
import { REVOKE_AFTER_MS, saveBytes, type DomLike } from './download'

function fakeDom() {
  let scheduled: { fn: () => void; ms: number } | undefined
  const created: Blob[] = []
  const dom: DomLike = {
    document: globalThis.document,
    URL: {
      createObjectURL: vi.fn((blob: Blob) => {
        created.push(blob)
        return 'blob:local-1'
      }),
      revokeObjectURL: vi.fn()
    },
    later: (fn, ms) => {
      scheduled = { fn, ms }
    }
  }
  return { dom, created, runScheduled: () => scheduled }
}

describe('saveBytes', () => {
  it('clicks a download link for a local Blob holding exactly the bytes', async () => {
    const { dom, created } = fakeDom()
    const bytes = randomBytes(64)
    const clicks: HTMLAnchorElement[] = []
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (
      this: HTMLAnchorElement
    ) {
      clicks.push(this)
    })

    saveBytes(bytes, 'recovery.ocbke', dom)

    expect(clicks).toHaveLength(1)
    expect(clicks[0]!.download).toBe('recovery.ocbke')
    expect(clicks[0]!.getAttribute('href')).toBe('blob:local-1')
    expect(created).toHaveLength(1)
    expect(new Uint8Array(await created[0]!.arrayBuffer())).toEqual(bytes)
    // The link is not left in the page.
    expect(document.querySelector('a[download]')).toBeNull()
    click.mockRestore()
  })

  it('revokes the object URL after a delay, even when the click throws', () => {
    const { dom, runScheduled } = fakeDom()
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {
      throw new Error('blocked')
    })

    expect(() => saveBytes(randomBytes(8), 'recovery.ocbke', dom)).toThrow('blocked')

    const scheduled = runScheduled()
    expect(scheduled?.ms).toBe(REVOKE_AFTER_MS)
    expect(dom.URL.revokeObjectURL).not.toHaveBeenCalled()
    scheduled!.fn()
    expect(dom.URL.revokeObjectURL).toHaveBeenCalledWith('blob:local-1')
    click.mockRestore()
  })
})
