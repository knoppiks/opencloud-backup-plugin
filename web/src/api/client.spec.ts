import { describe, expect, it, vi } from 'vitest'
import { BackupApi } from './client'
import { ApiError, isApiError } from './errors'

const BASE = 'https://cloud.example.org/backup/api/v1'
const SPACE = '28e0fe60-d4c1$7586a8a9!7586a8a9'

/** call is one recorded request. */
interface Call {
  url: string
  init: RequestInit
}

/** stub builds a fetch that answers with the given status and body. */
function stub(status: number, body?: unknown, contentType = 'application/json') {
  const calls: Call[] = []
  const fetchImpl = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
    calls.push({ url: String(url), init: init ?? {} })
    const text = body === undefined ? '' : typeof body === 'string' ? body : JSON.stringify(body)
    return new Response(text === '' ? null : text, {
      status,
      headers: { 'Content-Type': contentType }
    })
  })
  return { calls, fetchImpl: fetchImpl as unknown as typeof fetch }
}

function client(fetchImpl: typeof fetch) {
  return new BackupApi({ baseUrl: BASE, getToken: () => 'token-abc', fetchImpl })
}

/** signedOut is a client whose token source has nothing to give. */
function signedOut(fetchImpl: typeof fetch) {
  return new BackupApi({ baseUrl: BASE, getToken: () => undefined, fetchImpl })
}

describe('BackupApi requests', () => {
  it('sends the bearer token and asks for JSON', async () => {
    const { calls, fetchImpl } = stub(200, { spaces: [] })
    await client(fetchImpl).listSpaces()

    const headers = calls[0]!.init.headers as Record<string, string>
    expect(headers.Authorization).toBe('Bearer token-abc')
    expect(headers.Accept).toBe('application/json')
    // No body, so no content type: a GET with one invites a preflight, and
    // there is no CORS on the other side to answer it.
    expect(headers['Content-Type']).toBeUndefined()
  })

  // The auth store renews the token. A copy taken once would expire and every
  // call would 401 until the page reloaded.
  it('reads the token again for every request', async () => {
    const { calls, fetchImpl } = stub(200, { spaces: [] })
    const tokens = ['first', 'second']
    const api = new BackupApi({ baseUrl: BASE, getToken: () => tokens.shift(), fetchImpl })

    await api.listSpaces()
    await api.listSpaces()

    expect((calls[0]!.init.headers as Record<string, string>).Authorization).toBe('Bearer first')
    expect((calls[1]!.init.headers as Record<string, string>).Authorization).toBe('Bearer second')
  })

  it('awaits an async token source', async () => {
    const { calls, fetchImpl } = stub(200, { spaces: [] })
    const api = new BackupApi({
      baseUrl: BASE,
      getToken: () => Promise.resolve('async-token'),
      fetchImpl
    })
    await api.listSpaces()
    expect((calls[0]!.init.headers as Record<string, string>).Authorization).toBe(
      'Bearer async-token'
    )
  })

  it('fails with unauthorized rather than sending an anonymous request', async () => {
    const { calls, fetchImpl } = stub(200, { spaces: [] })
    await expect(signedOut(fetchImpl).listSpaces()).rejects.toMatchObject({
      code: 'unauthorized'
    })
    expect(calls).toHaveLength(0)
  })

  // Space ids look like "<mount>$<id>!<id>". "$" has to be escaped or it ends
  // the path segment as far as some proxies are concerned; "!" is a legal
  // sub-delimiter and stays, which is why this asserts the encoding rather than
  // the absence of punctuation.
  it('percent-encodes the space id', async () => {
    const { calls, fetchImpl } = stub(200, { configured: false })
    await client(fetchImpl).keyStatus(SPACE)

    expect(calls[0]!.url).toBe(`${BASE}/spaces/${encodeURIComponent(SPACE)}/backup/keystatus`)
    expect(calls[0]!.url).toContain('%24')
    expect(calls[0]!.url).not.toContain('$')
    // Whatever the escaping, the id must survive the round trip intact.
    const segment = calls[0]!.url.split('/spaces/')[1]!.split('/backup')[0]!
    expect(decodeURIComponent(segment)).toBe(SPACE)
  })

  it('omits an absent limit and sends one that is present', async () => {
    const withLimit = stub(200, { runs: [] })
    await client(withLimit.fetchImpl).listRuns(SPACE, 10)
    expect(withLimit.calls[0]!.url).toContain('?limit=10')

    const without = stub(200, { runs: [] })
    await client(without.fetchImpl).listRuns(SPACE)
    expect(without.calls[0]!.url).not.toContain('?')
  })

  it('unwraps the collection envelopes', async () => {
    const spaces = stub(200, { spaces: [{ id: 's', name: 'S', type: 'personal' }] })
    expect(await client(spaces.fetchImpl).listSpaces()).toHaveLength(1)

    const targets = stub(200, { targets: [{ id: 't', name: 'Buddy' }] })
    expect(await client(targets.fetchImpl).listTargets()).toEqual([{ id: 't', name: 'Buddy' }])

    const snapshots = stub(200, { snapshots: [] })
    expect(await client(snapshots.fetchImpl).listSnapshots(SPACE)).toEqual([])
  })

  // A missing key is not an empty list on the wire, but it is the same thing to
  // a template, and a crash in a v-for is a worse answer than "nothing yet".
  it('treats a missing collection key as empty', async () => {
    const { fetchImpl } = stub(200, {})
    expect(await client(fetchImpl).listSpaces()).toEqual([])
  })

  it('sends a JSON body with a content type on writes', async () => {
    const { calls, fetchImpl } = stub(200, { space_id: SPACE, enabled: true, cron: '0 3 * * *' })
    await client(fetchImpl).setSchedule(SPACE, { enabled: true, cron: '0 3 * * *' })

    const { init } = calls[0]!
    expect(init.method).toBe('PUT')
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/json')
    expect(JSON.parse(init.body as string)).toEqual({ enabled: true, cron: '0 3 * * *' })
  })

  // PATCH exists so an edit to one field does not re-send the others; a client
  // that filled in the rest "helpfully" would reintroduce the race it removes.
  it('sends only the fields given on a config patch', async () => {
    const { calls, fetchImpl } = stub(200, { space_id: SPACE, retention_days: 30 })
    await client(fetchImpl).patchBackupConfig(SPACE, { retention_days: 30 })

    const { url, init } = calls[0]!
    expect(url).toBe(`${BASE}/spaces/${encodeURIComponent(SPACE)}/backup/config`)
    expect(init.method).toBe('PATCH')
    expect(JSON.parse(init.body as string)).toEqual({ retention_days: 30 })
  })

  it('sends only a snapshot id on restore: the destination is not the client to choose', async () => {
    const { calls, fetchImpl } = stub(202, { job_id: 'j', space_id: SPACE, snapshot_id: 'snap' })
    await client(fetchImpl).restore(SPACE, 'snap')

    expect(JSON.parse(calls[0]!.init.body as string)).toEqual({ snapshot_id: 'snap' })
  })

  it('accepts a 202 body for a started run', async () => {
    const { fetchImpl } = stub(202, { job_id: 'job-1', space_id: SPACE, state: 'running' })
    expect(await client(fetchImpl).runBackup(SPACE)).toMatchObject({ job_id: 'job-1' })
  })
})

describe('BackupApi failures', () => {
  it('maps the error envelope to a typed code and keeps the status', async () => {
    const { fetchImpl } = stub(409, {
      error: { code: 'conflict', message: 'this space already has keys' }
    })
    const err = await client(fetchImpl)
      .setupKeys(SPACE, { wrapped_dk_rk: 'x', data_key: 'y' })
      .catch((e: unknown) => e)

    expect(isApiError(err)).toBe(true)
    expect(err).toMatchObject({
      code: 'conflict',
      status: 409,
      serverMessage: 'this space already has keys'
    })
  })

  it.each([
    [400, 'bad_request'],
    [401, 'unauthorized'],
    [403, 'forbidden'],
    [404, 'not_found'],
    [409, 'conflict'],
    [502, 'upstream_error'],
    [503, 'unavailable'],
    [500, 'internal_error']
  ])('falls back to the status when no envelope arrives: %i', async (status, code) => {
    const { fetchImpl } = stub(status, '<html>gateway error</html>', 'text/html')
    await expect(client(fetchImpl).listSpaces()).rejects.toMatchObject({ code, status })
  })

  // An ingress error page is not ours to show, and on a bad day it is the only
  // thing that answered.
  it('never puts a non-JSON error body into the message', async () => {
    const { fetchImpl } = stub(502, '<html>nginx: upstream unavailable</html>', 'text/html')
    const err = (await client(fetchImpl)
      .listSpaces()
      .catch((e: unknown) => e)) as ApiError

    expect(err.message).not.toContain('nginx')
    expect(err.serverMessage).toBeUndefined()
  })

  it('reports an unrecognised code as unknown rather than guessing', async () => {
    const { fetchImpl } = stub(418, { error: { code: 'teapot', message: 'no coffee' } })
    await expect(client(fetchImpl).listSpaces()).rejects.toMatchObject({
      code: 'unknown',
      serverMessage: 'no coffee'
    })
  })

  it('distinguishes a service that is unreachable from one that refused', async () => {
    const fetchImpl = vi.fn(() => Promise.reject(new TypeError('Failed to fetch')))
    const api = client(fetchImpl as unknown as typeof fetch)

    await expect(api.listSpaces()).rejects.toMatchObject({ code: 'offline', status: undefined })
  })

  it('reports a timeout as a timeout', async () => {
    const fetchImpl = vi.fn((_url: string | URL | Request, init?: RequestInit) => {
      return new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () =>
          reject(new DOMException('aborted', 'AbortError'))
        )
      })
    })
    const api = new BackupApi({
      baseUrl: BASE,
      getToken: () => 'token',
      fetchImpl: fetchImpl as unknown as typeof fetch,
      timeoutMs: 5
    })

    await expect(api.listSpaces()).rejects.toMatchObject({ code: 'timeout' })
  })

  it('rejects a 200 that is not JSON instead of returning undefined', async () => {
    const { fetchImpl } = stub(200, 'not json at all', 'text/plain')
    await expect(client(fetchImpl).listSpaces()).rejects.toMatchObject({
      code: 'malformed_response'
    })
  })

  it('classifies which failures are worth a retry button', async () => {
    const retryable = ['offline', 'timeout', 'unavailable', 'upstream_error']
    for (const code of retryable) {
      expect(new ApiError(code as never, 'x').isRetryable).toBe(true)
    }
    for (const code of ['bad_request', 'forbidden', 'conflict', 'not_found', 'unauthorized']) {
      expect(new ApiError(code as never, 'x').isRetryable).toBe(false)
    }
    expect(new ApiError('unauthorized', 'x').isAuth).toBe(true)
    expect(new ApiError('forbidden', 'x').isPermission).toBe(true)
  })
})

// Setup carries the Data Key and the recovery envelope. An error that quoted
// the body it failed on would put key material into a console and a bug report.
describe('BackupApi never echoes a request body', () => {
  it('keeps key material out of the thrown error', async () => {
    // Markers rather than realistic base64 blobs. The assertion only needs a
    // string distinctive enough to find, and a high-entropy literal sitting in
    // a variable called `dataKey` is indistinguishable from a real leak to a
    // secret scanner — which is a false alarm in the one file whose subject is
    // key material, and so the one file where a real alarm must be believed.
    const dataKey = 'data-key-that-must-never-be-echoed'
    const envelope = 'rk-envelope-that-must-never-be-echoed'
    const { fetchImpl } = stub(400, {
      error: { code: 'bad_request', message: 'argon2id parameters below the accepted minimum' }
    })

    const err = (await client(fetchImpl)
      .setupKeys(SPACE, { wrapped_dk_rk: envelope, data_key: dataKey })
      .catch((e: unknown) => e)) as ApiError

    const serialized = `${err.message}|${err.serverMessage}|${JSON.stringify(err)}|${err.stack}`
    expect(serialized).not.toContain(dataKey)
    expect(serialized).not.toContain(envelope)
  })

  it('sends no data key on the rotation path', async () => {
    const { calls, fetchImpl } = stub(200, { space_id: SPACE, configured: true })
    await client(fetchImpl).rotateRecoveryKey(SPACE, { wrapped_dk_rk: 'new-envelope' })

    const body = JSON.parse(calls[0]!.init.body as string) as Record<string, unknown>
    expect(Object.keys(body)).toEqual(['wrapped_dk_rk'])
    expect(body.data_key).toBeUndefined()
  })
})
