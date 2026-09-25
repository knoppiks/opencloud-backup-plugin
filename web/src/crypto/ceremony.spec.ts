// The ceremony's two promises, tested as promises rather than as code paths:
// the plaintext Recovery Key never reaches a request body, and an envelope that
// does not open with the key the user is about to be shown never reaches the
// server.
import { beforeEach, describe, expect, it, vi } from 'vitest'

// Mocked so the self-verification can be given something broken to catch. In
// production `seal` is the real one; the check exists precisely because no test
// runs against the code that will one day be wrong.
vi.mock('./envelope', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./envelope')>()
  return { ...actual, seal: vi.fn(actual.seal) }
})

import { base64Decode, bytesToHex, equalBytes, randomBytes } from './bytes'
import {
  checkRecoveryEnvelope,
  inspect,
  MIN_ARGON_PARAMS,
  seal,
  UnwrapError,
  WrapKind
} from './envelope'
import { decodeRecoveryKey, generateRecoveryKey, RecoveryKeyError } from './recoverykey'
import {
  CeremonyError,
  DK_SIZE,
  performRecoveryKeyRotation,
  performSetupCeremony,
  recoverDataKey,
  recoveryKeyOpens
} from './ceremony'

const sealMock = vi.mocked(seal)

// Every test seals for real; the floor parameters keep that affordable without
// weakening what is being tested — the API accepts exactly these.
const params = MIN_ARGON_PARAMS

beforeEach(() => {
  sealMock.mockClear()
})

describe('setup ceremony', () => {
  it('produces an envelope the displayed recovery key opens', async () => {
    const ceremony = await performSetupCeremony(params)

    expect(ceremony.dataKey).toHaveLength(DK_SIZE)
    const recovered = await recoverDataKey(ceremony.envelope, ceremony.recoveryKey)
    expect(equalBytes(recovered, ceremony.dataKey)).toBe(true)
  })

  it('produces an envelope the API will accept', async () => {
    const ceremony = await performSetupCeremony(params)

    const info = checkRecoveryEnvelope(ceremony.envelope)
    expect(info.kind).toBe(WrapKind.RK)
    expect(info.kdf).toBe('argon2id')
    expect(info.argon).toEqual(params)
  })

  it('never puts the recovery key in the request', async () => {
    const ceremony = await performSetupCeremony(params)

    // Both the string the user sees and the bytes behind it. The E2E suite
    // makes the same assertion at the network layer; this is the version that
    // fails in milliseconds, on every change.
    const body = JSON.stringify(ceremony.request)
    expect(body).not.toContain(ceremony.recoveryKey)
    expect(body).not.toContain(ceremony.recoveryKey.replaceAll('-', ''))
    expect(body).not.toContain(bytesToHex(decodeRecoveryKey(ceremony.recoveryKey)))
  })

  it('sends the data key and the envelope, and nothing else', async () => {
    const ceremony = await performSetupCeremony(params)

    expect(Object.keys(ceremony.request).sort()).toEqual(['data_key', 'wrapped_dk_rk'])
    expect(equalBytes(base64Decode(ceremony.request.data_key), ceremony.dataKey)).toBe(true)
    expect(equalBytes(base64Decode(ceremony.request.wrapped_dk_rk), ceremony.envelope)).toBe(true)
  })

  it('refuses to finish when the envelope does not open with the shown key', async () => {
    // A plausible bug: the envelope is sealed under a different secret than the
    // one displayed. Structurally perfect, policy-compliant, and worthless.
    sealMock.mockImplementationOnce(async (options) => {
      const actual = await vi.importActual<typeof import('./envelope')>('./envelope')
      return await actual.seal({ ...options, secret: randomBytes(20) })
    })

    await expect(performSetupCeremony(params)).rejects.toThrow(
      expect.objectContaining({ name: 'CeremonyError', code: 'self_verify_failed' })
    )
  })

  it('refuses to finish when the envelope wraps a different data key', async () => {
    // The subtler bug: the key opens the envelope, but what comes out is not
    // the Data Key the Space will actually be backed up with. Every status
    // stays green until a restore is attempted.
    sealMock.mockImplementationOnce(async (options) => {
      const actual = await vi.importActual<typeof import('./envelope')>('./envelope')
      return await actual.seal({ ...options, plaintext: randomBytes(DK_SIZE) })
    })

    await expect(performSetupCeremony(params)).rejects.toThrow(CeremonyError)
  })

  it('gives every space its own keys', async () => {
    const first = await performSetupCeremony(params)
    const second = await performSetupCeremony(params)

    expect(first.recoveryKey).not.toBe(second.recoveryKey)
    expect(equalBytes(first.dataKey, second.dataKey)).toBe(false)
  })
})

describe('recovery key rotation', () => {
  it('re-wraps the same data key under a new key', async () => {
    const setup = await performSetupCeremony(params)
    const dataKey = Uint8Array.from(setup.dataKey)

    const rotation = await performRecoveryKeyRotation({
      currentEnvelope: setup.envelope,
      currentRecoveryKey: setup.recoveryKey,
      params
    })

    expect(rotation.recoveryKey).not.toBe(setup.recoveryKey)
    const recovered = await recoverDataKey(rotation.envelope, rotation.recoveryKey)
    // Same Data Key: existing backups stay readable and nothing is re-uploaded.
    expect(equalBytes(recovered, dataKey)).toBe(true)
  })

  it('never sends the data key', async () => {
    const setup = await performSetupCeremony(params)
    const rotation = await performRecoveryKeyRotation({
      currentEnvelope: setup.envelope,
      currentRecoveryKey: setup.recoveryKey,
      params
    })

    expect(Object.keys(rotation.request)).toEqual(['wrapped_dk_rk'])
    expect(JSON.stringify(rotation.request)).not.toContain(bytesToHex(setup.dataKey))
  })

  it('retires the old recovery key', async () => {
    const setup = await performSetupCeremony(params)
    const rotation = await performRecoveryKeyRotation({
      currentEnvelope: setup.envelope,
      currentRecoveryKey: setup.recoveryKey,
      params
    })

    await expect(recoverDataKey(rotation.envelope, setup.recoveryKey)).rejects.toThrow(UnwrapError)
  })

  it('fails on a wrong current key before generating anything', async () => {
    const setup = await performSetupCeremony(params)
    const other = await performSetupCeremony(params)
    sealMock.mockClear()

    await expect(
      performRecoveryKeyRotation({
        currentEnvelope: setup.envelope,
        currentRecoveryKey: other.recoveryKey,
        params
      })
    ).rejects.toThrow(UnwrapError)
    // The wrong key was the only thing consumed; no envelope was sealed, so
    // nothing could be offered to the user as their new key.
    expect(sealMock).not.toHaveBeenCalled()
  })

  it('fails on a mistyped current key without touching the KDF', async () => {
    const setup = await performSetupCeremony(params)

    await expect(
      performRecoveryKeyRotation({
        currentEnvelope: setup.envelope,
        currentRecoveryKey: setup.recoveryKey.slice(0, -1) + 'X',
        params
      })
    ).rejects.toThrow(RecoveryKeyError)
  })
})

describe('recoverDataKey', () => {
  it('rejects a correctly-opened envelope that does not hold a data key', async () => {
    // An RK envelope whose plaintext is the wrong size opens perfectly well.
    // Handing those 16 bytes on as a Data Key would fail much later, inside
    // kopia, as an unopenable repository.
    const recoveryKey = generateRecoveryKey()
    const envelope = await seal({
      plaintext: randomBytes(16),
      secret: recoveryKey.secret,
      kind: WrapKind.RK,
      params
    })
    expect(inspect(envelope).kind).toBe(WrapKind.RK)

    await expect(recoverDataKey(envelope, recoveryKey.display)).rejects.toThrow(
      expect.objectContaining({ name: 'CeremonyError', code: 'bad_data_key' })
    )
  })

  it('opens an envelope the user typed by hand', async () => {
    const ceremony = await performSetupCeremony(params)
    const retyped = ceremony.recoveryKey.toLowerCase().replaceAll('-', ' ')

    const recovered = await recoverDataKey(ceremony.envelope, retyped)
    expect(equalBytes(recovered, ceremony.dataKey)).toBe(true)
  })
})

// The wizard's answer to "did my setup land?" after an ambiguous failure: the
// key the user saved either opens what the server stored, or it does not.
describe('recoveryKeyOpens', () => {
  it('says yes for the key the envelope was made with, however it was typed', async () => {
    const ceremony = await performSetupCeremony(params)
    expect(await recoveryKeyOpens(ceremony.envelope, ceremony.recoveryKey)).toBe(true)
    expect(await recoveryKeyOpens(ceremony.envelope, ceremony.recoveryKey.toLowerCase())).toBe(true)
  })

  it('says no for another key, a mistyped key and garbage', async () => {
    const ceremony = await performSetupCeremony(params)
    const other = generateRecoveryKey().display
    const mistyped =
      ceremony.recoveryKey.slice(0, -1) + (ceremony.recoveryKey.endsWith('0') ? '2' : '0')

    expect(await recoveryKeyOpens(ceremony.envelope, other)).toBe(false)
    expect(await recoveryKeyOpens(ceremony.envelope, mistyped)).toBe(false)
    expect(await recoveryKeyOpens(ceremony.envelope, 'not a key')).toBe(false)
  })

  it('throws for an envelope that is not an envelope', async () => {
    const { recoveryKey } = await performSetupCeremony(params)
    await expect(recoveryKeyOpens(new Uint8Array(8), recoveryKey)).rejects.toThrow()
  })
})
