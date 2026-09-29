import { beforeEach, describe, expect, it, vi } from 'vitest'

const host = vi.hoisted(() => ({
  can: vi.fn(),
  listUsers: vi.fn(),
  getUser: vi.fn(),
  accessToken: 'token-abc' as string | undefined
}))

vi.mock('@opencloud-eu/web-pkg', () => ({
  useAbility: () => ({ can: host.can }),
  useClientService: () => ({
    graphAuthenticated: { users: { listUsers: host.listUsers, getUser: host.getUser } }
  }),
  useCapabilityStore: () => ({ sharingSearchMinLength: 3 }),
  useAuthStore: () => ({
    get accessToken() {
      return host.accessToken
    }
  })
}))

vi.mock('../appconfig', () => ({ apiBaseUrl: () => 'https://cloud.example.org/backup/api/v1' }))

import { useAdminApi } from './useAdminApi'
import { useIsAdmin } from './useIsAdmin'
import { useUserDirectory, useUserSearchMinLength } from './useUserDirectory'

beforeEach(() => {
  vi.clearAllMocks()
})

describe('useIsAdmin', () => {
  it('asks for read-all on Setting, the rule web-pkg uses for admin UI', () => {
    host.can.mockReturnValue(true)
    expect(useIsAdmin()).toBe(true)
    expect(host.can).toHaveBeenCalledWith('read-all', 'Setting')

    host.can.mockReturnValue(false)
    expect(useIsAdmin()).toBe(false)
  })
})

describe('useUserDirectory', () => {
  it('searches through the host graph client', async () => {
    host.listUsers.mockResolvedValue([{ id: 'u-1', displayName: 'Alice' }])
    expect(await useUserDirectory().search('ali')).toEqual([{ id: 'u-1', displayName: 'Alice' }])
    expect(host.listUsers).toHaveBeenCalled()
  })

  it('takes the minimum search length from the capabilities', () => {
    expect(useUserSearchMinLength()).toBe(3)
  })
})

describe('useAdminApi', () => {
  it('reads the token per request from the auth store', async () => {
    const fetchImpl = vi.fn(
      async (_url: string, _init: RequestInit) => new Response('{"targets":[]}', { status: 200 })
    )
    vi.stubGlobal('fetch', fetchImpl)
    try {
      const api = useAdminApi()
      host.accessToken = 'renewed'
      await api.listTargets()
      const init = fetchImpl.mock.calls[0]![1]
      expect((init.headers as Record<string, string>).Authorization).toBe('Bearer renewed')
    } finally {
      vi.unstubAllGlobals()
    }
  })
})
