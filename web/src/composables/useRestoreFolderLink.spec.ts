import { describe, expect, it, vi } from 'vitest'

const HOST_ID = '28e0fe60$9399d706'
const SERVICE_ID = `${HOST_ID}!9399d706`

const host = vi.hoisted(() => ({ spaces: {} as Record<string, { id: string }> }))

vi.mock('@opencloud-eu/web-pkg', () => ({
  useSpacesStore: () => ({ getSpace: (id: string) => host.spaces[id] }),
  createFileRouteOptions: (space: { id: string }, target: { path: string }) => ({
    space: space.id,
    path: target.path
  }),
  createLocationSpaces: (name: string, options: unknown) => ({ name, options })
}))

import { useRestoreFolderLink } from './useRestoreFolderLink'

describe('useRestoreFolderLink', () => {
  // The service's id and the host's differ in form for the same Space (8f).
  it('finds the Space the host holds under its graph id', () => {
    host.spaces = { [HOST_ID]: { id: HOST_ID } }
    expect(useRestoreFolderLink()(SERVICE_ID, 'Restore/x')).toEqual({
      name: 'files-spaces-generic',
      options: { space: HOST_ID, path: 'Restore/x' }
    })
  })

  it('prefers an exact match', () => {
    host.spaces = { [SERVICE_ID]: { id: SERVICE_ID }, [HOST_ID]: { id: HOST_ID } }
    expect(useRestoreFolderLink()(SERVICE_ID, 'Restore/x')).toMatchObject({
      options: { space: SERVICE_ID }
    })
  })

  it('gives no link for a Space the host does not know', () => {
    host.spaces = {}
    expect(useRestoreFolderLink()(SERVICE_ID, 'Restore/x')).toBeUndefined()
  })
})
