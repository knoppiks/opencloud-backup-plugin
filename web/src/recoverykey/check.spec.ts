// "Check my Recovery Key" without Vue: what it answers, what it refuses to
// guess, and that the key it is given goes nowhere but keyOpens.
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import {
  base64Encode,
  generateRecoveryKey,
  MIN_ARGON_PARAMS,
  randomBytes,
  seal,
  WrapKind
} from '../crypto'
import { fakeApi, space, SPACE_ID, status, type FakeApi } from '../test/fixtures'
import {
  ENVELOPE_FILE_NAME,
  RecoveryKeyCheck,
  type CheckApi,
  type CheckDeps,
  type CheckState
} from './check'

let envelope: Uint8Array
let api: FakeApi
let keyOpens: ReturnType<typeof vi.fn>
let saveFile: ReturnType<typeof vi.fn>

beforeAll(async () => {
  // A real envelope, so the machine's own "is this an RK envelope" check runs
  // against the real parser. What it wraps does not matter: keyOpens is fake.
  const key = generateRecoveryKey()
  envelope = await seal({
    plaintext: randomBytes(32),
    secret: key.secret,
    kind: WrapKind.RK,
    params: MIN_ARGON_PARAMS
  })
})

beforeEach(() => {
  api = fakeApi()
  keyOpens = vi.fn()
  saveFile = vi.fn()
})

function machine(role = 'viewer', keys = true): RecoveryKeyCheck {
  api.listSpaces.mockResolvedValue([space({ role })])
  api.status.mockResolvedValue(status({ keys_configured: keys }))
  api.recoveryEnvelope.mockResolvedValue({ envelope: base64Encode(envelope) })
  const deps: CheckDeps = {
    api: api as unknown as CheckApi,
    keyOpens: keyOpens as CheckDeps['keyOpens'],
    saveFile: saveFile as CheckDeps['saveFile'],
    yieldToRender: () => Promise.resolve(),
    onChange: () => {}
  }
  return new RecoveryKeyCheck(SPACE_ID, deps)
}

function ready(m: RecoveryKeyCheck): Extract<CheckState, { step: 'ready' }> {
  expect(m.state.step).toBe('ready')
  return m.state as Extract<CheckState, { step: 'ready' }>
}

const aKey = () => generateRecoveryKey().display

describe('start', () => {
  it('is ready for any member, and offers replacement to managers only', async () => {
    for (const [role, mayReplace] of [
      ['viewer', false],
      ['editor', false],
      ['manager', true],
      ['owner', true]
    ] as const) {
      const m = machine(role)
      await m.start()
      expect(ready(m).mayReplace, role).toBe(mayReplace)
      expect(m.spaceName).toBe('Family photos')
    }
  })

  it('has nothing to check for a Space without keys', async () => {
    const m = machine('manager', false)
    await m.start()
    expect(m.state.step).toBe('not_set_up')
    expect(api.recoveryEnvelope).not.toHaveBeenCalled()
  })

  it('fails the load when the Space is not in the caller’s list', async () => {
    const m = machine()
    api.listSpaces.mockResolvedValue([])
    await m.start()
    expect(m.state).toMatchObject({ step: 'load_failed', error: { code: 'not_found' } })
  })

  it('fails the load when a read fails', async () => {
    const m = machine()
    api.status.mockRejectedValue(new ApiError('offline', 'x'))
    await m.start()
    expect(m.state).toMatchObject({ step: 'load_failed', error: { code: 'offline' } })
  })
})

describe('check', () => {
  it('says the key opens the backups when it does', async () => {
    const m = machine()
    await m.start()
    const key = aKey()
    keyOpens.mockResolvedValue(true)
    await m.check(key)
    expect(ready(m).result).toEqual({ kind: 'opens' })
    expect(keyOpens).toHaveBeenCalledWith(envelope, key)
  })

  it('says a well-formed key that does not open is the wrong key', async () => {
    const m = machine()
    await m.start()
    keyOpens.mockResolvedValue(false)
    await m.check(aKey())
    expect(ready(m).result).toEqual({ kind: 'wrong' })
  })

  it('calls a typo malformed, before any request and before Argon2id', async () => {
    const m = machine()
    await m.start()
    const key = aKey()
    // One group swapped for another: the checksum catches it.
    const groups = key.split('-')
    const typo = [groups[0], groups[2], groups[1], ...groups.slice(3)].join('-')
    for (const input of ['', 'not a recovery key', typo]) {
      await m.check(input)
      expect(ready(m).result, input).toEqual({ kind: 'malformed' })
    }
    expect(api.recoveryEnvelope).not.toHaveBeenCalled()
    expect(keyOpens).not.toHaveBeenCalled()
  })

  it('accepts what decode accepts: lower case, spaces, no prefix', async () => {
    const m = machine()
    await m.start()
    keyOpens.mockResolvedValue(true)
    const key = aKey()
    const loose = key.split('-').slice(1).join(' ').toLowerCase()
    await m.check(loose)
    expect(ready(m).result).toEqual({ kind: 'opens' })
  })

  it('cannot tell when the envelope cannot be fetched, and never says wrong', async () => {
    const m = machine()
    await m.start()
    api.recoveryEnvelope.mockRejectedValueOnce(new ApiError('timeout', 'x'))
    await m.check(aKey())
    expect(ready(m).result).toMatchObject({ kind: 'cannot_tell', error: { code: 'timeout' } })
    expect(keyOpens).not.toHaveBeenCalled()
  })

  it('cannot tell when the stored envelope is not a recovery envelope', async () => {
    const m = machine()
    await m.start()
    for (const stored of [base64Encode(randomBytes(40)), 'not base64 at all!']) {
      api.recoveryEnvelope.mockResolvedValueOnce({ envelope: stored })
      await m.check(aKey())
      expect(ready(m).result).toMatchObject({
        kind: 'cannot_tell',
        error: { code: 'malformed_response' }
      })
    }
    expect(keyOpens).not.toHaveBeenCalled()
  })

  it('cannot tell when unwrapping throws rather than answering', async () => {
    const m = machine()
    await m.start()
    keyOpens.mockRejectedValue(new Error('envelope parse'))
    await m.check(aKey())
    expect(ready(m).result).toMatchObject({ kind: 'cannot_tell' })
  })

  it('runs one check at a time', async () => {
    const m = machine()
    await m.start()
    let release: (v: boolean) => void = () => {}
    keyOpens.mockReturnValue(new Promise<boolean>((r) => (release = r)))
    const first = m.check(aKey())
    await vi.waitFor(() => expect(keyOpens).toHaveBeenCalledTimes(1))
    expect(ready(m).checking).toBe(true)
    await m.check(aKey())
    expect(keyOpens).toHaveBeenCalledTimes(1)
    release(true)
    await first
    expect(ready(m)).toMatchObject({ checking: false, result: { kind: 'opens' } })
  })

  it('clearResult forgets the answer', async () => {
    const m = machine()
    await m.start()
    await m.check('nonsense')
    m.clearResult()
    expect(ready(m).result).toBeUndefined()
  })

  it('stays closed when a check finishes after dispose', async () => {
    const m = machine()
    await m.start()
    let release: (v: boolean) => void = () => {}
    keyOpens.mockReturnValue(new Promise<boolean>((r) => (release = r)))
    const pending = m.check(aKey())
    await vi.waitFor(() => expect(keyOpens).toHaveBeenCalled())
    m.dispose()
    release(true)
    await pending
    expect(m.state).toEqual({ step: 'closed' })
  })
})

describe('the Recovery Key goes nowhere', () => {
  it('is not in state and not in any API call, for every outcome', async () => {
    const m = machine('manager')
    const seen: string[] = []
    ;(m as unknown as { deps: CheckDeps }).deps.onChange = (s) => seen.push(JSON.stringify(s))
    await m.start()
    const keys = [aKey(), aKey(), aKey()]
    keyOpens.mockResolvedValueOnce(true).mockResolvedValueOnce(false)
    await m.check(keys[0]!)
    await m.check(keys[1]!)
    api.recoveryEnvelope.mockRejectedValueOnce(new ApiError('offline', 'x'))
    await m.check(keys[2]!)
    await m.download()

    for (const key of keys) {
      const groups = key.split('-').slice(1)
      const haystacks = [
        ...seen,
        JSON.stringify(m),
        ...Object.values(api).flatMap((fn) => fn.mock.calls.map((c) => JSON.stringify(c))),
        ...saveFile.mock.calls.map((c) => JSON.stringify(c))
      ]
      for (const hay of haystacks) {
        expect(hay).not.toContain(key)
        expect(hay).not.toContain(groups.join(''))
        for (const group of groups) {
          expect(hay).not.toContain(group)
        }
      }
    }
  })
})

describe('download', () => {
  it('saves exactly the stored envelope as recovery.ocbke', async () => {
    const m = machine()
    await m.start()
    await m.download()
    expect(saveFile).toHaveBeenCalledTimes(1)
    const [bytes, name] = saveFile.mock.calls[0] as [Uint8Array, string]
    expect(name).toBe(ENVELOPE_FILE_NAME)
    expect(name).toBe('recovery.ocbke')
    expect(bytes).toEqual(envelope)
    expect(ready(m).download).toEqual({ kind: 'done' })
  })

  it('reports a failed fetch and saves nothing', async () => {
    const m = machine()
    await m.start()
    api.recoveryEnvelope.mockRejectedValueOnce(new ApiError('offline', 'x'))
    await m.download()
    expect(saveFile).not.toHaveBeenCalled()
    expect(ready(m).download).toMatchObject({ kind: 'failed', error: { code: 'offline' } })
  })

  it('refuses to save something that is not a recovery envelope', async () => {
    const m = machine()
    await m.start()
    api.recoveryEnvelope.mockResolvedValueOnce({ envelope: base64Encode(randomBytes(40)) })
    await m.download()
    expect(saveFile).not.toHaveBeenCalled()
    expect(ready(m).download).toMatchObject({
      kind: 'failed',
      error: { code: 'malformed_response' }
    })
  })
})
