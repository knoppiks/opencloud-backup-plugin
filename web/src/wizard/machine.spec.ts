// The setup wizard's machine, without Vue and without Argon2id.
//
// The ceremony is faked: its own spec (crypto/ceremony.spec.ts) proves the
// real one self-verifies. What is tested here is what the wizard does with a
// ceremony's result — and above all what it does *not* do with it.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type BackupStatus, type Target } from '../api'
import { base64Encode, CeremonyError, generateRecoveryKey, type SetupCeremony } from '../crypto'
import { fakeApi, space, SPACE_ID, status, type FakeApi } from '../test/fixtures'
import {
  DEFAULT_SCHEDULE,
  gateMatches,
  nextStep,
  pickGateGroups,
  SetupWizard,
  type WizardApi,
  type WizardDeps,
  type WizardState
} from './machine'

const ONE: Target = { id: 't-1', name: 'Buddy' }
const TWO: Target = { id: 't-2', name: 'Offsite' }

/** fresh is a Space nobody has set up: no binding, no keys, no history. */
function fresh(overrides: Partial<BackupStatus> = {}): BackupStatus {
  const s = status({ configured: false, keys_configured: false, enabled: false, ...overrides })
  delete s.last_run
  delete s.last_successful_run
  delete s.next_run
  return s
}

let api: FakeApi
let states: WizardState[]
let ceremonies: SetupCeremony[]
let ceremony: ReturnType<typeof vi.fn>
let keyOpens: ReturnType<typeof vi.fn>

/** fakeCeremony is a real-shaped result with a real Recovery Key, no Argon2id. */
function fakeCeremony(): SetupCeremony {
  const recoveryKey = generateRecoveryKey().display
  const dataKey = Uint8Array.from({ length: 32 }, (_, i) => i + 1)
  const envelope = new Uint8Array([1, 2, 3])
  const result = {
    recoveryKey,
    dataKey,
    envelope,
    request: { wrapped_dk_rk: base64Encode(envelope), data_key: base64Encode(dataKey) }
  }
  ceremonies.push(result)
  return result
}

function deps(overrides: Partial<WizardDeps> = {}): WizardDeps {
  return {
    api: api as unknown as WizardApi,
    ceremony: ceremony as unknown as WizardDeps['ceremony'],
    keyOpens: keyOpens as unknown as WizardDeps['keyOpens'],
    randomInt: (bound) => bound - 1,
    yieldToRender: () => Promise.resolve(),
    onChange: (s) => states.push(s),
    ...overrides
  }
}

function wizard(role = 'manager', randomInt = (bound: number) => bound - 1): SetupWizard {
  api.listSpaces.mockResolvedValue([space({ role })])
  return new SetupWizard(SPACE_ID, deps({ randomInt }))
}

/** groupsOf answers the gate correctly for whatever it asked. */
function answersFor(w: SetupWizard): string[] {
  const s = w.state
  if (s.step !== 'confirm') throw new Error(`not at the gate: ${s.step}`)
  const groups = s.recoveryKey.split('-').slice(1)
  return s.groups.map((g) => groups[g] as string)
}

/** toGate drives a manager from key_intro to the gate. */
async function toGate(w: SetupWizard): Promise<void> {
  await w.createKey()
  w.keySaved()
}

beforeEach(() => {
  api = fakeApi()
  states = []
  ceremonies = []
  ceremony = vi.fn(() => Promise.resolve(fakeCeremony()))
  keyOpens = vi.fn(() => Promise.resolve(true))
  api.listTargets.mockResolvedValue([ONE])
  api.setBackupConfig.mockResolvedValue({})
  api.setupKeys.mockResolvedValue({ configured: true })
  api.setSchedule.mockResolvedValue({})
})

describe('nextStep (the resume rule)', () => {
  it.each([
    [{ configured: false, keys_configured: false, enabled: false }, 'target'],
    [{ configured: false, keys_configured: true, enabled: false }, 'target'],
    [{ configured: true, keys_configured: false, enabled: false }, 'keys'],
    // Enabled without keys (set through the API): keys are still missing.
    [{ configured: true, keys_configured: false, enabled: true }, 'keys'],
    [{ configured: true, keys_configured: true, enabled: false }, 'schedule'],
    [{ configured: true, keys_configured: true, enabled: true }, 'done']
  ])('%o resumes at %s', (flags, step) => {
    expect(nextStep(status(flags))).toBe(step)
  })
})

describe('step order', () => {
  it('binds the target with runs off, then keys, then switches runs on', async () => {
    api.status.mockResolvedValueOnce(fresh())
    const w = wizard()
    await w.start()
    expect(w.state.step).toBe('key_intro')

    await toGate(w)
    await w.confirmKey(answersFor(w))
    expect(w.state.step).toBe('schedule')

    api.status.mockResolvedValueOnce(status({ next_run: '2026-09-25T00:30:00Z' }))
    await w.saveSchedule()
    expect(w.state.step).toBe('done')

    expect(api.setBackupConfig).toHaveBeenCalledWith(SPACE_ID, {
      target_id: 't-1',
      retention_days: 0,
      enabled: false
    })
    expect(api.setSchedule).toHaveBeenCalledWith(SPACE_ID, {
      enabled: true,
      preset: { kind: 'daily', hour: 2, minute: 30 }
    })
    const order = [api.setBackupConfig, api.setupKeys, api.setSchedule].map(
      (m) => m.mock.invocationCallOrder[0]
    )
    expect(order).toEqual([...order].sort((a, b) => (a ?? 0) - (b ?? 0)))
  })

  it('sends weekday only with a weekly preset, and Sunday is 0', async () => {
    api.status.mockResolvedValueOnce(status({ enabled: false }))
    const w = wizard()
    await w.start()
    w.chooseSchedule({ kind: 'weekly', hour: 4, minute: 15, weekday: 0 })
    api.status.mockResolvedValueOnce(status())
    await w.saveSchedule()

    expect(api.setSchedule).toHaveBeenCalledWith(SPACE_ID, {
      enabled: true,
      preset: { kind: 'weekly', hour: 4, minute: 15, weekday: 0 }
    })
  })

  it('preselects daily at 02:30 and passes the service zone through', async () => {
    const unset = status({ enabled: false, timezone: 'Europe/Berlin' })
    delete unset.preset
    api.status.mockResolvedValueOnce(unset)
    const w = wizard()
    await w.start()
    expect(w.state).toMatchObject({
      step: 'schedule',
      choice: DEFAULT_SCHEDULE,
      timezone: 'Europe/Berlin'
    })
  })

  it('keeps the schedule form and says why when saving fails', async () => {
    api.status.mockResolvedValueOnce(status({ enabled: false }))
    api.setSchedule.mockRejectedValueOnce(new ApiError('offline', 'x'))
    const w = wizard()
    await w.start()
    await w.saveSchedule()
    expect(w.state).toMatchObject({ step: 'schedule', saving: false })
    expect(w.state.step === 'schedule' && w.state.error?.code).toBe('offline')
  })
})

describe('resume from server state', () => {
  it('skips the target step once a target is bound', async () => {
    api.status.mockResolvedValueOnce(status({ keys_configured: false, enabled: false }))
    const w = wizard()
    await w.start()
    expect(w.state.step).toBe('key_intro')
    expect(api.listTargets).not.toHaveBeenCalled()
    expect(api.setBackupConfig).not.toHaveBeenCalled()
  })

  it('never offers the ceremony when keys exist', async () => {
    api.status.mockResolvedValueOnce(status({ enabled: false }))
    const w = wizard()
    await w.start()
    expect(w.state.step).toBe('schedule')

    await w.createKey()
    expect(ceremony).not.toHaveBeenCalled()
    expect(states.some((s) => s.step === 'key_intro' || s.step === 'generating')).toBe(false)
  })

  it('shows a finished setup as done, writing nothing', async () => {
    api.status.mockResolvedValueOnce(status())
    const w = wizard()
    await w.start()
    expect(w.state.step).toBe('done')
    expect(api.setBackupConfig).not.toHaveBeenCalled()
    expect(api.setSchedule).not.toHaveBeenCalled()
  })

  it('binds a target for a Space that has keys but lost its binding, then schedules', async () => {
    api.status.mockResolvedValueOnce(fresh({ keys_configured: true }))
    const w = wizard()
    await w.start()
    expect(w.state.step).toBe('schedule')
    expect(ceremony).not.toHaveBeenCalled()
  })

  it('fails the load for a Space not in the caller’s list', async () => {
    api.status.mockResolvedValueOnce(fresh())
    const w = wizard()
    api.listSpaces.mockResolvedValue([space({ id: 'other' })])
    await w.start()
    expect(w.state.step === 'load_failed' && w.state.error.code).toBe('not_found')
  })
})

describe('targets', () => {
  it('auto-selects the only target and skips the picker', async () => {
    api.status.mockResolvedValueOnce(fresh())
    const w = wizard()
    await w.start()
    expect(states.some((s) => s.step === 'pick_target' && !s.saving)).toBe(false)
    expect(api.setBackupConfig).toHaveBeenCalledTimes(1)
  })

  it('shows a picker for several, and writes only after a choice', async () => {
    api.status.mockResolvedValueOnce(fresh())
    api.listTargets.mockResolvedValue([ONE, TWO])
    const w = wizard()
    await w.start()
    expect(w.state).toMatchObject({ step: 'pick_target', selected: undefined })

    await w.confirmTarget()
    expect(api.setBackupConfig).not.toHaveBeenCalled()

    w.selectTarget('t-2')
    await w.confirmTarget()
    expect(api.setBackupConfig).toHaveBeenCalledWith(SPACE_ID, {
      target_id: 't-2',
      retention_days: 0,
      enabled: false
    })
    expect(w.state.step).toBe('key_intro')
  })

  it('cannot proceed without a granted target', async () => {
    api.status.mockResolvedValueOnce(fresh())
    api.listTargets.mockResolvedValue([])
    const w = wizard()
    await w.start()
    expect(w.state.step).toBe('no_targets')
    expect(api.setBackupConfig).not.toHaveBeenCalled()
  })

  it('shows a refused binding on the picker, with the target kept', async () => {
    api.status.mockResolvedValueOnce(fresh())
    api.setBackupConfig.mockRejectedValueOnce(new ApiError('forbidden', 'x', 403))
    const w = wizard()
    await w.start()
    expect(w.state).toMatchObject({ step: 'pick_target', selected: 't-1', saving: false })
    expect(w.state.step === 'pick_target' && w.state.error?.code).toBe('forbidden')
  })
})

describe('roles', () => {
  it('offers a viewer nothing and writes nothing', async () => {
    api.status.mockResolvedValueOnce(fresh())
    const w = wizard('viewer')
    await w.start()
    expect(w.state.step).toBe('not_allowed')
    expect(api.listTargets).not.toHaveBeenCalled()
  })

  it('lets an editor bind a target, then stops at the keys', async () => {
    api.status.mockResolvedValueOnce(fresh())
    const w = wizard('editor')
    await w.start()
    expect(api.setBackupConfig).toHaveBeenCalled()
    expect(w.state.step).toBe('needs_manager')

    await w.createKey()
    expect(ceremony).not.toHaveBeenCalled()
  })

  it('lets an editor finish the schedule once a manager has made the keys', async () => {
    api.status.mockResolvedValueOnce(status({ enabled: false }))
    const w = wizard('editor')
    await w.start()
    expect(w.state.step).toBe('schedule')
  })
})

describe('confirmation gate', () => {
  it('picks two distinct groups of seven, in key order', () => {
    const seen = new Set<string>()
    for (let seed = 0; seed < 50; seed++) {
      let n = seed
      const groups = pickGateGroups((bound) => n++ % bound)
      expect(groups).toHaveLength(2)
      expect(new Set(groups).size).toBe(2)
      expect(groups.every((g) => g >= 0 && g < 7)).toBe(true)
      expect(groups).toEqual([...groups].sort((a, b) => a - b))
      seen.add(groups.join())
    }
    expect(seen.size).toBeGreaterThan(1)
  })

  it('matches with decode’s tolerance and nothing looser', () => {
    // Assembled from its groups rather than written as one literal: a
    // key-shaped string in the source is what gitleaks exists to flag.
    const key = ['ocbk1', 'AB1C0', 'DEFGH', 'JKMNP', 'QRSTV', 'WXYZ0', '12345', '6789'].join('-')
    expect(gateMatches(key, [0, 6], ['ab1c0', '6789'])).toBe(true)
    expect(gateMatches(key, [0, 6], ['a b l c o', '67-89'])).toBe(true)
    expect(gateMatches(key, [0, 6], ['AB1C0', '6788'])).toBe(false)
    expect(gateMatches(key, [0, 6], ['AB1C0'])).toBe(false)
    expect(gateMatches(key, [0, 6], ['DEFGH', 'AB1C0'])).toBe(false)
  })

  it('cannot be skipped: setup is sent only from a matching gate', async () => {
    api.status.mockResolvedValueOnce(status({ keys_configured: false, enabled: false }))
    const w = wizard()
    await w.start()
    await w.createKey()
    expect(w.state.step).toBe('show_key')

    // No path from the key screen to the POST except through the gate.
    await w.confirmKey(['anything', 'at all'])
    expect(api.setupKeys).not.toHaveBeenCalled()

    w.keySaved()
    expect(w.state.step).toBe('confirm')
    expect(api.setupKeys).not.toHaveBeenCalled()
  })

  it('blocks setup on a wrong group, and says so', async () => {
    api.status.mockResolvedValueOnce(status({ keys_configured: false, enabled: false }))
    const w = wizard()
    await w.start()
    await toGate(w)

    const [first] = answersFor(w)
    await w.confirmKey([first as string, 'ZZZZZ'])
    expect(w.state).toMatchObject({ step: 'confirm', mismatch: true })
    expect(api.setupKeys).not.toHaveBeenCalled()

    await w.confirmKey(answersFor(w))
    expect(api.setupKeys).toHaveBeenCalledTimes(1)
  })

  it('can show the key again, and asks for new groups afterwards', async () => {
    api.status.mockResolvedValueOnce(status({ keys_configured: false, enabled: false }))
    let n = 0
    const w = wizard('manager', (bound) => n++ % bound)
    await w.start()
    await toGate(w)
    const first = w.state.step === 'confirm' ? w.state.groups : []

    w.showKeyAgain()
    expect(w.state.step).toBe('show_key')
    w.keySaved()
    expect(w.state.step === 'confirm' && w.state.groups).not.toEqual(first)
  })
})

describe('the ceremony', () => {
  beforeEach(() => {
    api.status.mockResolvedValue(status({ keys_configured: false, enabled: false }))
  })

  it('says it is working before Argon2id takes the main thread', async () => {
    const w = wizard()
    await w.start()
    let seenWhenYielding: string | undefined
    const yielding = new SetupWizard(
      SPACE_ID,
      deps({
        yieldToRender: () => {
          seenWhenYielding = yielding.state.step
          return Promise.resolve()
        }
      })
    )
    await yielding.start()
    await yielding.createKey()
    expect(seenWhenYielding).toBe('generating')
    expect(w.state.step).toBe('key_intro')
  })

  it('a self-verify failure is a failed ceremony: no key shown, nothing sent', async () => {
    ceremony.mockRejectedValueOnce(new CeremonyError('self_verify_failed', 'x'))
    const w = wizard()
    await w.start()
    await w.createKey()

    expect(w.state).toMatchObject({ step: 'key_intro', failure: { kind: 'ceremony' } })
    expect(states.some((s) => s.step === 'show_key')).toBe(false)
    expect(api.setupKeys).not.toHaveBeenCalled()

    // Nothing was sent, so trying again is safe.
    await w.createKey()
    expect(w.state.step).toBe('show_key')
  })

  it('zeroizes the Data Key when setup succeeds', async () => {
    const w = wizard()
    await w.start()
    await toGate(w)
    const dataKey = ceremonies[0]!.dataKey
    const fill = vi.spyOn(dataKey, 'fill')

    await w.confirmKey(answersFor(w))
    expect(api.setupKeys).toHaveBeenCalledWith(SPACE_ID, ceremonies[0]!.request)
    expect(fill).toHaveBeenCalledWith(0)
    expect(dataKey.every((b) => b === 0)).toBe(true)
  })

  it.each([
    ['409 conflict', new ApiError('conflict', 'x', 409)],
    ['400 too weak', new ApiError('bad_request', 'too weak', 400)],
    ['offline', new ApiError('offline', 'x')],
    ['503', new ApiError('unavailable', 'x', 503)]
  ])('zeroizes the Data Key when setup fails (%s)', async (_, error) => {
    api.setupKeys.mockRejectedValueOnce(error)
    const w = wizard()
    await w.start()
    await toGate(w)
    const dataKey = ceremonies[0]!.dataKey

    await w.confirmKey(answersFor(w))
    expect(dataKey.every((b) => b === 0)).toBe(true)
  })

  it('zeroizes a Data Key that was never sent when the wizard closes', async () => {
    const w = wizard()
    await w.start()
    await toGate(w)
    const dataKey = ceremonies[0]!.dataKey

    w.dispose()
    expect(dataKey.every((b) => b === 0)).toBe(true)
    expect(w.state).toEqual({ step: 'closed' })
  })

  it('forgets the Recovery Key once setup has landed', async () => {
    const w = wizard()
    await w.start()
    await toGate(w)
    const key = ceremonies[0]!.recoveryKey
    await w.confirmKey(answersFor(w))

    expect(JSON.stringify(w.state)).not.toContain(key)
  })
})

describe('setup answers', () => {
  beforeEach(() => {
    api.status.mockResolvedValueOnce(status({ keys_configured: false, enabled: false }))
  })

  it('409 is "already protected", terminal, with no way back to the ceremony', async () => {
    api.setupKeys.mockRejectedValueOnce(new ApiError('conflict', 'x', 409))
    const w = wizard()
    await w.start()
    await toGate(w)
    await w.confirmKey(answersFor(w))

    expect(w.state).toEqual({ step: 'already_protected' })
    await w.createKey()
    await w.retryUncertain()
    expect(ceremony).toHaveBeenCalledTimes(1)
    expect(api.setupKeys).toHaveBeenCalledTimes(1)
    expect(w.state).toEqual({ step: 'already_protected' })
  })

  it('a 400 is shown as a refusal, and the shown key is to be discarded', async () => {
    const refusal = new ApiError('bad_request', 'recovery envelope is too weak', 400)
    api.setupKeys.mockRejectedValueOnce(refusal)
    const w = wizard()
    await w.start()
    await toGate(w)
    await w.confirmKey(answersFor(w))

    expect(w.state).toEqual({
      step: 'key_intro',
      failure: { kind: 'refused', error: refusal },
      discardPrevious: true
    })
  })
})

describe('an ambiguous setup failure', () => {
  async function uncertain(): Promise<{ w: SetupWizard; key: string }> {
    api.status.mockResolvedValueOnce(status({ keys_configured: false, enabled: false }))
    api.setupKeys.mockRejectedValueOnce(new ApiError('timeout', 'x'))
    const w = wizard()
    await w.start()
    await toGate(w)
    await w.confirmKey(answersFor(w))
    const key = ceremonies[0]!.recoveryKey
    expect(w.state).toMatchObject({ step: 'setup_uncertain', recoveryKey: key })
    return { w, key }
  }

  it('keeps the saved key, not the Data Key', async () => {
    await uncertain()
    expect(ceremonies[0]!.dataKey.every((b) => b === 0)).toBe(true)
  })

  it('did not land: a new ceremony, and the saved key is to be discarded', async () => {
    const { w } = await uncertain()
    api.status.mockResolvedValueOnce(status({ keys_configured: false, enabled: false }))
    await w.retryUncertain()

    expect(w.state).toEqual({ step: 'key_intro', failure: undefined, discardPrevious: true })
    expect(api.recoveryEnvelope).not.toHaveBeenCalled()
  })

  it('landed: the saved key opens what is stored, so setup continues', async () => {
    const { w, key } = await uncertain()
    api.status.mockResolvedValueOnce(status({ enabled: false }))
    api.recoveryEnvelope.mockResolvedValueOnce({ envelope: base64Encode(new Uint8Array([9, 9])) })
    await w.retryUncertain()

    expect(keyOpens).toHaveBeenCalledWith(new Uint8Array([9, 9]), key)
    expect(w.state.step).toBe('schedule')
    expect(JSON.stringify(w.state)).not.toContain(key)
  })

  it('someone else set it up: the saved key does not open it', async () => {
    const { w } = await uncertain()
    api.status.mockResolvedValueOnce(status({ enabled: false }))
    api.recoveryEnvelope.mockResolvedValueOnce({ envelope: base64Encode(new Uint8Array([9])) })
    keyOpens.mockResolvedValueOnce(false)
    await w.retryUncertain()

    expect(w.state).toEqual({ step: 'already_protected' })
  })

  it('stays uncertain, key kept, when the check itself fails', async () => {
    const { w, key } = await uncertain()
    api.status.mockRejectedValueOnce(new ApiError('offline', 'x'))
    await w.retryUncertain()
    expect(w.state).toMatchObject({ step: 'setup_uncertain', recoveryKey: key, checking: false })
  })
})

describe('the Recovery Key never reaches the API', () => {
  it('appears in no argument of any call, through setup and an uncertain retry', async () => {
    api.status.mockResolvedValueOnce(fresh())
    api.setupKeys.mockRejectedValueOnce(new ApiError('offline', 'x'))
    const w = wizard()
    await w.start()
    await toGate(w)
    await w.confirmKey(answersFor(w))
    api.status.mockResolvedValueOnce(status({ enabled: false }))
    api.recoveryEnvelope.mockResolvedValueOnce({ envelope: base64Encode(new Uint8Array([1])) })
    await w.retryUncertain()
    api.status.mockResolvedValueOnce(status())
    await w.saveSchedule()
    expect(w.state.step).toBe('done')

    const key = ceremonies[0]!.recoveryKey
    const bare = key.split('-').slice(1).join('')
    for (const [name, method] of Object.entries(api)) {
      for (const args of method.mock.calls) {
        const sent = JSON.stringify(args)
        expect(sent, name).not.toContain(key)
        expect(sent, name).not.toContain(bare)
        for (const group of key.split('-').slice(1)) {
          expect(sent, name).not.toContain(group)
        }
      }
    }
  })
})
