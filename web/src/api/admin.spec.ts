import { describe, expect, it } from 'vitest'
import { AdminApi } from './admin'
import type { ApiError } from './errors'
import type { AdminTargetRequest } from './types'
import { stubFetch } from '../test/fetchstub'

const BASE = 'https://cloud.example.org/backup/api/v1'

function admin(fetchImpl: typeof fetch) {
  return new AdminApi({ baseUrl: BASE, getToken: () => 'token-abc', fetchImpl })
}

// Markers, not realistic keys: see client.spec.ts for why.
const SECRET = 'secret-marker-that-must-never-be-echoed'

function request(overrides: Partial<AdminTargetRequest> = {}): AdminTargetRequest {
  return {
    name: 'Buddy',
    endpoint: 'buddy.example.org:3900',
    bucket: 'backups',
    region: '',
    prefix: '',
    use_path_style: true,
    disable_tls: false,
    credentials: { access_key_id: 'access-marker', secret_access_key: SECRET },
    ...overrides
  }
}

describe('AdminApi routes', () => {
  it('lists targets, unwrapping the envelope', async () => {
    const { calls, fetchImpl } = stubFetch(200, { targets: [{ id: 't', name: 'Buddy' }] })
    expect(await admin(fetchImpl).listTargets()).toEqual([{ id: 't', name: 'Buddy' }])
    expect(calls[0]!.url).toBe(`${BASE}/admin/targets`)
    expect(calls[0]!.init.method).toBe('GET')
  })

  it('treats a missing collection key as empty', async () => {
    const { fetchImpl } = stubFetch(200, {})
    expect(await admin(fetchImpl).listTargets()).toEqual([])
    expect(await admin(fetchImpl).grants('t')).toEqual([])
    expect(await admin(fetchImpl).checkTarget(request())).toEqual([])
  })

  it('reads one target with its id encoded', async () => {
    const { calls, fetchImpl } = stubFetch(200, { id: 'a/b', name: 'X' })
    await admin(fetchImpl).target('a/b')
    expect(calls[0]!.url).toBe(`${BASE}/admin/targets/a%2Fb`)
  })

  it('creates with POST and the body as given', async () => {
    const { calls, fetchImpl } = stubFetch(201, { id: 'new', name: 'Buddy' })
    const created = await admin(fetchImpl).createTarget(request())

    expect(created).toMatchObject({ id: 'new' })
    expect(calls[0]!.url).toBe(`${BASE}/admin/targets`)
    expect(calls[0]!.init.method).toBe('POST')
    expect(JSON.parse(calls[0]!.init.body as string)).toEqual(request())
  })

  it('updates with PUT on the target', async () => {
    const { calls, fetchImpl } = stubFetch(200, { id: 't', name: 'Buddy' })
    const body = request()
    delete body.credentials
    await admin(fetchImpl).updateTarget('t', body)

    expect(calls[0]!.url).toBe(`${BASE}/admin/targets/t`)
    expect(calls[0]!.init.method).toBe('PUT')
    // Absent, not null: the server reads a missing pair as "keep the stored ones".
    expect(JSON.parse(calls[0]!.init.body as string)).not.toHaveProperty('credentials')
  })

  it('deletes with DELETE and accepts the empty 204', async () => {
    const { calls, fetchImpl } = stubFetch(204)
    await expect(admin(fetchImpl).deleteTarget('t')).resolves.toBeUndefined()
    expect(calls[0]!.init.method).toBe('DELETE')
    expect(calls[0]!.url).toBe(`${BASE}/admin/targets/t`)
  })

  it('checks with POST to the check route and returns the results', async () => {
    const results = [{ role: 'backup', outcome: 'ok' }]
    const { calls, fetchImpl } = stubFetch(200, { results })
    expect(await admin(fetchImpl).checkTarget(request())).toEqual(results)
    expect(calls[0]!.url).toBe(`${BASE}/admin/targets/check`)
    expect(calls[0]!.init.method).toBe('POST')
  })

  it('reads and replaces grants as one list', async () => {
    const grants = [{ scope: 'user' as const, user_id: 'u-1' }]
    const { calls, fetchImpl } = stubFetch(200, { grants })

    expect(await admin(fetchImpl).grants('t')).toEqual(grants)
    expect(await admin(fetchImpl).replaceGrants('t', grants)).toEqual(grants)

    expect(calls[0]!.url).toBe(`${BASE}/admin/targets/t/grants`)
    expect(calls[1]!.init.method).toBe('PUT')
    expect(JSON.parse(calls[1]!.init.body as string)).toEqual({ grants })
  })

  // An empty list revokes the target from everyone. It must reach the server
  // as an empty list, not as a missing key the server might read differently.
  it('sends an empty grant list as an empty list', async () => {
    const { calls, fetchImpl } = stubFetch(200, { grants: [] })
    await admin(fetchImpl).replaceGrants('t', [])
    expect(JSON.parse(calls[0]!.init.body as string)).toEqual({ grants: [] })
  })
})

describe('AdminApi failures', () => {
  it('maps target_in_use and keeps the server message with its count', async () => {
    const message = '2 space(s) still back up to this target; point them elsewhere first'
    const { fetchImpl } = stubFetch(409, { error: { code: 'target_in_use', message } })
    await expect(admin(fetchImpl).deleteTarget('t')).rejects.toMatchObject({
      code: 'target_in_use',
      status: 409,
      serverMessage: message
    })
  })

  it('keeps the submitted secret out of the thrown error', async () => {
    const { fetchImpl } = stubFetch(400, {
      error: { code: 'bad_request', message: 'bucket is required' }
    })
    const err = (await admin(fetchImpl)
      .createTarget(request())
      .catch((e: unknown) => e)) as ApiError

    const serialized = `${err.message}|${err.serverMessage}|${JSON.stringify(err)}|${err.stack}`
    expect(serialized).not.toContain(SECRET)
  })
})
