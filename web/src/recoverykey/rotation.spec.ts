// Replacing a Recovery Key without Vue: the order of things, the gate, every
// way the POST can end, and above all where the two keys do *not* go.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import {
  base64Encode,
  CeremonyError,
  EnvelopeError,
  generateRecoveryKey,
  randomBytes,
  RecoveryKeyError,
  UnwrapError,
  type RotationCeremony,
  type RotationOptions
} from '../crypto'
import { fakeApi, space, SPACE_ID, status, type FakeApi } from '../test/fixtures'
import {
  RecoveryKeyRotation,
  type RotationApi,
  type RotationDeps,
  type RotationState
} from './rotation'

let api: FakeApi
let rotate: ReturnType<typeof vi.fn>
let keyOpens: ReturnType<typeof vi.fn>
let states: RotationState[]
let stored: Uint8Array
let ceremonies: RotationCeremony[]

/** OLD_KEY is what the user types as the current key; generated, never a literal. */
let OLD_KEY: string

beforeEach(() => {
  api = fakeApi()
  states = []
  ceremonies = []
  stored = randomBytes(90)
  OLD_KEY = generateRecoveryKey().display
  rotate = vi.fn(async (options: RotationOptions): Promise<RotationCeremony> => {
    const envelope = randomBytes(90)
    const ceremony: RotationCeremony = {
      recoveryKey: generateRecoveryKey().display,
      envelope,
      request: {
        wrapped_dk_rk: base64Encode(envelope),
        replaces_sha256: `digest-${options.currentEnvelope.length}`
      }
    }
    ceremonies.push(ceremony)
    return ceremony
  })
  keyOpens = vi.fn()
})

function machine(role = 'manager', keys = true): RecoveryKeyRotation {
  api.listSpaces.mockResolvedValue([space({ role })])
  api.status.mockResolvedValue(status({ keys_configured: keys }))
  api.recoveryEnvelope.mockResolvedValue({ envelope: base64Encode(stored) })
  const deps: RotationDeps = {
    api: api as unknown as RotationApi,
    rotate: rotate as RotationDeps['rotate'],
    keyOpens: keyOpens as RotationDeps['keyOpens'],
    randomInt: () => 0,
    yieldToRender: () => Promise.resolve(),
    onChange: (s) => states.push(s)
  }
  return new RecoveryKeyRotation(SPACE_ID, deps)
}

/** answersFor types back the groups the gate asked for. */
function answersFor(m: RecoveryKeyRotation): string[] {
  const s = m.state
  if (s.step !== 'confirm') {
    throw new Error(`not at the gate: ${s.step}`)
  }
  const groups = s.recoveryKey.split('-').slice(1)
  return s.groups.map((g) => groups[g] as string)
}

async function toGate(m: RecoveryKeyRotation): Promise<void> {
  await m.start()
  await m.useCurrentKey(OLD_KEY)
  m.keySaved()
}

async function throughGate(m: RecoveryKeyRotation): Promise<void> {
  await toGate(m)
  await m.confirmKey(answersFor(m))
}

describe('start', () => {
  it('asks a manager or owner for the current key', async () => {
    for (const role of ['manager', 'owner']) {
      const m = machine(role)
      await m.start()
      expect(m.state).toEqual({ step: 'enter_current', failure: undefined, discardNew: false })
    }
  })

  it('tells a viewer or editor that a manager has to do this', async () => {
    for (const role of ['viewer', 'editor']) {
      const m = machine(role)
      await m.start()
      expect(m.state.step, role).toBe('needs_manager')
    }
  })

  it('has nothing to replace without keys', async () => {
    const m = machine('manager', false)
    await m.start()
    expect(m.state.step).toBe('not_set_up')
  })

  it('fails the load when a read fails or the Space is not listed', async () => {
    const m = machine()
    api.status.mockRejectedValue(new ApiError('offline', 'x'))
    await m.start()
    expect(m.state).toMatchObject({ step: 'load_failed', error: { code: 'offline' } })

    const n = machine()
    api.listSpaces.mockResolvedValue([])
    await n.start()
    expect(n.state).toMatchObject({ step: 'load_failed', error: { code: 'not_found' } })
  })
})

describe('the current key', () => {
  it('unwraps what the server stores with what the user typed, then shows a new key', async () => {
    const m = machine()
    await m.start()
    await m.useCurrentKey(OLD_KEY)
    expect(rotate).toHaveBeenCalledWith({ currentEnvelope: stored, currentRecoveryKey: OLD_KEY })
    expect(m.state).toEqual({ step: 'show_key', recoveryKey: ceremonies[0]!.recoveryKey })
    expect(api.rotateRecoveryKey).not.toHaveBeenCalled()
  })

  it.each([
    ['a typo', new RecoveryKeyError('checksum', 'x'), { kind: 'malformed' }],
    ['the wrong key', new UnwrapError(), { kind: 'wrong' }],
    ['a key that opens to no Data Key', new CeremonyError('bad_data_key', 'x'), { kind: 'wrong' }],
    ['a failed self-verify', new CeremonyError('self_verify_failed', 'x'), { kind: 'ceremony' }],
    ['a broken stored envelope', new EnvelopeError('malformed', 'x'), { kind: 'envelope' }],
    ['anything else', new Error('boom'), { kind: 'ceremony' }]
  ])('asks again after %s, and sends nothing', async (_, thrown, failure) => {
    const m = machine()
    await m.start()
    rotate.mockRejectedValueOnce(thrown)
    await m.useCurrentKey(OLD_KEY)
    expect(m.state).toEqual({ step: 'enter_current', failure, discardNew: false })
    expect(api.rotateRecoveryKey).not.toHaveBeenCalled()
    expect(JSON.stringify(m)).not.toContain(OLD_KEY)
  })

  it('asks again when the envelope cannot be read, without trying the key', async () => {
    const m = machine()
    await m.start()
    api.recoveryEnvelope.mockRejectedValueOnce(new ApiError('timeout', 'x'))
    await m.useCurrentKey(OLD_KEY)
    expect(m.state).toMatchObject({
      step: 'enter_current',
      failure: { kind: 'request', error: { code: 'timeout' } }
    })
    api.recoveryEnvelope.mockResolvedValueOnce({ envelope: '%%% not base64' })
    await m.useCurrentKey(OLD_KEY)
    expect(m.state).toMatchObject({ step: 'enter_current', failure: { kind: 'envelope' } })
    expect(rotate).not.toHaveBeenCalled()
  })

  it('does nothing for an editor, whatever the view does', async () => {
    const m = machine('editor')
    await m.start()
    await m.useCurrentKey(OLD_KEY)
    expect(rotate).not.toHaveBeenCalled()
    expect(api.recoveryEnvelope).not.toHaveBeenCalled()
  })

  it('keeps nothing when disposed while the ceremony runs', async () => {
    const m = machine()
    await m.start()
    let release: () => void = () => {}
    const gate = new Promise<void>((r) => (release = r))
    rotate.mockImplementationOnce(async (o: RotationOptions) => {
      await gate
      return {
        recoveryKey: generateRecoveryKey().display,
        envelope: o.currentEnvelope,
        request: { wrapped_dk_rk: 'x', replaces_sha256: 'y' }
      }
    })
    const pending = m.useCurrentKey(OLD_KEY)
    await vi.waitFor(() => expect(rotate).toHaveBeenCalled())
    m.dispose()
    release()
    await pending
    expect(m.state).toEqual({ step: 'closed' })
    expect(JSON.stringify(m)).not.toContain(OLD_KEY)
  })
})

describe('the gate', () => {
  it('cannot be skipped: the rotation is sent only from matching answers', async () => {
    const m = machine()
    await m.start()
    await m.useCurrentKey(OLD_KEY)
    await m.confirmKey(['anything', 'at all'])
    expect(api.rotateRecoveryKey).not.toHaveBeenCalled()

    m.keySaved()
    await m.confirmKey(['AAAAA', 'BBBBB'])
    expect(m.state).toMatchObject({ step: 'confirm', mismatch: true })
    expect(api.rotateRecoveryKey).not.toHaveBeenCalled()

    m.showKeyAgain()
    expect(m.state.step).toBe('show_key')
    m.keySaved()
    api.rotateRecoveryKey.mockResolvedValueOnce({})
    await m.confirmKey(answersFor(m))
    expect(api.rotateRecoveryKey).toHaveBeenCalledTimes(1)
  })

  it('sends exactly the ceremony’s request: the new envelope and the old digest', async () => {
    const m = machine()
    api.rotateRecoveryKey.mockResolvedValueOnce({})
    await throughGate(m)
    expect(api.rotateRecoveryKey).toHaveBeenCalledWith(SPACE_ID, ceremonies[0]!.request)
    const [, body] = api.rotateRecoveryKey.mock.calls[0] as [string, object]
    expect(Object.keys(body).sort()).toEqual(['replaces_sha256', 'wrapped_dk_rk'])
  })
})

describe('after the POST', () => {
  it('is done on success, and forgets both keys', async () => {
    const m = machine()
    api.rotateRecoveryKey.mockResolvedValueOnce({})
    await throughGate(m)
    expect(m.state).toEqual({ step: 'done', mayRunBackup: true, backup: { kind: 'idle' } })
    expect(JSON.stringify(m)).not.toContain(OLD_KEY)
    expect(JSON.stringify(m)).not.toContain(ceremonies[0]!.recoveryKey)
  })

  it('says someone else replaced it on 409, and forgets both keys', async () => {
    const m = machine()
    api.rotateRecoveryKey.mockRejectedValueOnce(new ApiError('conflict', 'x', 409))
    await throughGate(m)
    expect(m.state).toEqual({ step: 'replaced_elsewhere' })
    expect(JSON.stringify(m)).not.toContain(OLD_KEY)
    expect(JSON.stringify(m)).not.toContain(ceremonies[0]!.recoveryKey)
  })

  it('starts over after a refusal, telling the user to discard the new key', async () => {
    const m = machine()
    api.rotateRecoveryKey.mockRejectedValueOnce(new ApiError('bad_request', 'x', 400))
    await throughGate(m)
    expect(m.state).toMatchObject({
      step: 'enter_current',
      discardNew: true,
      failure: { kind: 'request', error: { code: 'bad_request' } }
    })
    expect(JSON.stringify(m)).not.toContain(OLD_KEY)
  })

  it.each([
    ['offline', new ApiError('offline', 'x')],
    ['a timeout', new ApiError('timeout', 'x')],
    ['a 5xx', new ApiError('internal_error', 'x', 500)]
  ])('is uncertain after %s, and keeps both keys out of state', async (_, error) => {
    const m = machine()
    api.rotateRecoveryKey.mockRejectedValueOnce(error)
    await throughGate(m)
    expect(m.state).toEqual({ step: 'uncertain', error, checking: false })
    expect(JSON.stringify(m.state)).not.toContain(OLD_KEY)
    expect(JSON.stringify(m.state)).not.toContain(ceremonies[0]!.recoveryKey)
  })
})

describe('settling an uncertain rotation', () => {
  async function uncertain(): Promise<RecoveryKeyRotation> {
    const m = machine()
    api.rotateRecoveryKey.mockRejectedValueOnce(new ApiError('offline', 'x'))
    await throughGate(m)
    expect(m.state.step).toBe('uncertain')
    return m
  }

  it('is done when the new key opens what is stored now', async () => {
    const m = await uncertain()
    const now = randomBytes(90)
    api.recoveryEnvelope.mockResolvedValueOnce({ envelope: base64Encode(now) })
    keyOpens.mockResolvedValueOnce(true)
    await m.checkAgain()
    expect(keyOpens).toHaveBeenCalledWith(now, ceremonies[0]!.recoveryKey)
    expect(m.state.step).toBe('done')
    expect(JSON.stringify(m)).not.toContain(OLD_KEY)
  })

  it('starts over, discarding the new key, when the old key still opens it', async () => {
    const m = await uncertain()
    keyOpens.mockResolvedValueOnce(false).mockResolvedValueOnce(true)
    await m.checkAgain()
    expect(keyOpens).toHaveBeenLastCalledWith(stored, OLD_KEY)
    expect(m.state).toEqual({ step: 'enter_current', failure: undefined, discardNew: true })
    expect(JSON.stringify(m)).not.toContain(OLD_KEY)
    expect(JSON.stringify(m)).not.toContain(ceremonies[0]!.recoveryKey)
  })

  it('says someone else replaced it when neither key opens it', async () => {
    const m = await uncertain()
    keyOpens.mockResolvedValue(false)
    await m.checkAgain()
    expect(m.state).toEqual({ step: 'replaced_elsewhere' })
    expect(JSON.stringify(m)).not.toContain(OLD_KEY)
  })

  it('stays uncertain, keeping both keys, when the envelope cannot be read', async () => {
    const m = await uncertain()
    api.recoveryEnvelope.mockRejectedValueOnce(new ApiError('offline', 'y'))
    await m.checkAgain()
    expect(m.state).toMatchObject({ step: 'uncertain', checking: false, error: { message: 'y' } })

    api.recoveryEnvelope.mockResolvedValueOnce({ envelope: '%%%' })
    await m.checkAgain()
    expect(m.state).toMatchObject({ step: 'uncertain', error: { code: 'malformed_response' } })

    keyOpens.mockRejectedValueOnce(new EnvelopeError('malformed', 'x'))
    await m.checkAgain()
    expect(m.state).toMatchObject({ step: 'uncertain', error: { code: 'malformed_response' } })

    // Still able to settle it afterwards: both keys were kept.
    keyOpens.mockResolvedValueOnce(false).mockResolvedValueOnce(true)
    await m.checkAgain()
    expect(m.state).toMatchObject({ step: 'enter_current', discardNew: true })
  })
})

describe('back up now', () => {
  async function done(role = 'manager'): Promise<RecoveryKeyRotation> {
    const m = machine(role)
    api.rotateRecoveryKey.mockResolvedValueOnce({})
    await throughGate(m)
    return m
  }

  it('starts a run', async () => {
    const m = await done()
    api.runBackup.mockResolvedValueOnce({})
    await m.backUpNow()
    expect(api.runBackup).toHaveBeenCalledWith(SPACE_ID)
    expect(m.state).toMatchObject({ backup: { kind: 'started' } })
  })

  it('treats a run already under way as started', async () => {
    const m = await done()
    api.runBackup.mockRejectedValueOnce(new ApiError('run_in_progress', 'x', 409))
    await m.backUpNow()
    expect(m.state).toMatchObject({ backup: { kind: 'started' } })
  })

  it('reports any other failure', async () => {
    const m = await done()
    api.runBackup.mockRejectedValueOnce(new ApiError('forbidden', 'x', 403))
    await m.backUpNow()
    expect(m.state).toMatchObject({ backup: { kind: 'failed', error: { code: 'forbidden' } } })
  })
})

describe('the Recovery Keys never reach the API or the old one the state', () => {
  it('holds for a full run through uncertainty, a retry and a backup', async () => {
    const m = machine()
    // First attempt: refused, start over. Second: uncertain, then settled.
    api.rotateRecoveryKey
      .mockRejectedValueOnce(new ApiError('bad_request', 'x', 400))
      .mockRejectedValueOnce(new ApiError('offline', 'x'))
    await throughGate(m)
    await m.useCurrentKey(OLD_KEY)
    m.keySaved()
    await m.confirmKey(answersFor(m))
    keyOpens.mockResolvedValueOnce(true)
    await m.checkAgain()
    api.runBackup.mockResolvedValueOnce({})
    await m.backUpNow()
    expect(m.state).toMatchObject({ step: 'done', backup: { kind: 'started' } })

    const keys = [OLD_KEY, ...ceremonies.map((c) => c.recoveryKey)]
    expect(keys).toHaveLength(3)
    const calls = Object.entries(api).flatMap(([name, fn]) =>
      fn.mock.calls.map((args) => [name, JSON.stringify(args)] as const)
    )
    for (const key of keys) {
      const groups = key.split('-').slice(1)
      for (const [name, sent] of calls) {
        expect(sent, name).not.toContain(key)
        expect(sent, name).not.toContain(groups.join(''))
        for (const group of groups) {
          expect(sent, name).not.toContain(group)
        }
      }
    }
    for (const s of states) {
      expect(JSON.stringify(s)).not.toContain(OLD_KEY)
    }
    expect(JSON.stringify(m)).not.toContain(OLD_KEY)
  })
})
