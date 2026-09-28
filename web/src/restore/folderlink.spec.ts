import { createFileRouteOptions, createLocationSpaces } from '@opencloud-eu/web-pkg'
import type { SpaceResource } from '@opencloud-eu/web-client'
import { describe, expect, it } from 'vitest'
import { isRestoreFolder, restoreFolderLocation } from './folderlink'

const FOLDER = 'Restore/2026-09-25T03-00-00Z'

describe('isRestoreFolder', () => {
  it('accepts what the service writes', () => {
    expect(isRestoreFolder(FOLDER)).toBe(true)
  })

  // The value is rendered as a link into someone's Files app, so anything that
  // would point outside the restore area is refused rather than trusted.
  it.each([
    ['empty', ''],
    ['the root itself', 'Restore'],
    ['the root with a slash', 'Restore/'],
    ['absolute', '/Restore/x'],
    ['a parent step', 'Restore/..'],
    ['a current step', 'Restore/.'],
    ['nested', 'Restore/a/b'],
    ['escaping', 'Restore/../Photos'],
    ['another folder', 'Photos/2026'],
    ['a backslash', 'Restore\\..\\x'],
    ['case-folded root', 'restore/x']
  ])('refuses %s', (_, folder) => {
    expect(isRestoreFolder(folder)).toBe(false)
  })
})

describe('restoreFolderLocation', () => {
  const build = (space: string, path: string) => `${space}:${path}`

  it('builds a route when the host knows the Space', () => {
    expect(restoreFolderLocation(FOLDER, () => 'space', build)).toBe(`space:${FOLDER}`)
  })

  it('gives no route when the host has not loaded the Space', () => {
    expect(restoreFolderLocation(FOLDER, () => undefined, build)).toBeUndefined()
  })

  it('gives no route when the lookup throws', () => {
    const throwing = () => {
      throw new Error('store not ready')
    }
    expect(restoreFolderLocation(FOLDER, throwing, build)).toBeUndefined()
  })

  it('never asks for the Space for a folder it will not link', () => {
    let asked = false
    restoreFolderLocation(
      'Restore/../x',
      () => {
        asked = true
        return 'space'
      },
      build
    )
    expect(asked).toBe(false)
  })

  // Pinned against the host's real helpers, so a change in how web-pkg builds
  // a Files route shows up here rather than as a dead link in a browser.
  it('builds the Files-app route the host expects', () => {
    const space = {
      id: 'storage$space',
      driveAlias: 'project/family-photos',
      getDriveAliasAndItem: ({ path }: { path: string }) =>
        `project/family-photos/${path.replace(/^\//, '')}`
    } as unknown as SpaceResource

    const location = restoreFolderLocation(
      FOLDER,
      () => space,
      (s, path) => createLocationSpaces('files-spaces-generic', createFileRouteOptions(s, { path }))
    )

    expect(location).toMatchObject({
      name: 'files-spaces-generic',
      params: { driveAliasAndItem: `project/family-photos/${FOLDER}` }
    })
  })
})
