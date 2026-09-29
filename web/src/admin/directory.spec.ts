import { describe, expect, it, vi } from 'vitest'
import { graphUserDirectory, searchTerm, type GraphUsersClient } from './directory'

function fakeGraph(overrides: Partial<GraphUsersClient> = {}): GraphUsersClient {
  return {
    getUser: vi.fn(() => Promise.reject(new Error('unexpected'))),
    listUsers: vi.fn(() => Promise.reject(new Error('unexpected'))),
    ...overrides
  }
}

describe('graphUserDirectory.search', () => {
  it('asks graph for a quoted term, by display name', async () => {
    const listUsers = vi.fn(async () => [{ id: 'u-1', displayName: 'Alice', mail: 'a@x.org' }])
    const directory = graphUserDirectory(fakeGraph({ listUsers }))

    expect(await directory.search('  ali ')).toEqual([
      { id: 'u-1', displayName: 'Alice', mail: 'a@x.org' }
    ])
    expect(listUsers).toHaveBeenCalledWith({
      search: '"ali"',
      orderBy: ['displayName'],
      select: ['id', 'displayName', 'mail']
    })
  })

  it('does not ask at all for an empty term', async () => {
    const listUsers = vi.fn()
    expect(await graphUserDirectory(fakeGraph({ listUsers })).search(' "" ')).toEqual([])
    expect(listUsers).not.toHaveBeenCalled()
  })

  it('skips users without an id and names the nameless by id', async () => {
    const listUsers = vi.fn(async () => [{ displayName: 'Ghost' }, { id: 'u-2', mail: null }])
    expect(await graphUserDirectory(fakeGraph({ listUsers })).search('x')).toEqual([
      { id: 'u-2', displayName: 'u-2' }
    ])
  })

  it('lets a failed search fail: the picker says so', async () => {
    const listUsers = vi.fn(() => Promise.reject(new Error('down')))
    await expect(graphUserDirectory(fakeGraph({ listUsers })).search('x')).rejects.toThrow()
  })
})

describe('graphUserDirectory.lookup', () => {
  it('names a user by id', async () => {
    const getUser = vi.fn(async () => ({ id: 'u-1', displayName: 'Alice' }))
    expect(await graphUserDirectory(fakeGraph({ getUser })).lookup('u-1')).toEqual({
      id: 'u-1',
      displayName: 'Alice'
    })
    expect(getUser).toHaveBeenCalledWith('u-1', { select: ['id', 'displayName', 'mail'] })
  })

  // A deleted account must not break the list or make its grant unremovable.
  it('answers undefined for a user that cannot be named', async () => {
    const getUser = vi.fn(() => Promise.reject(new Error('404')))
    expect(await graphUserDirectory(fakeGraph({ getUser })).lookup('gone')).toBeUndefined()
  })
})

describe('searchTerm', () => {
  it('trims and drops double quotes', () => {
    expect(searchTerm(' "a"b ')).toBe('ab')
  })
})
