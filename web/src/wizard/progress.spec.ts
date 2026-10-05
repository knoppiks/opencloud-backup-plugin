import { describe, expect, it } from 'vitest'
import { ApiError } from '../api'
import { status } from '../test/fixtures'
import { DEFAULT_SCHEDULE, type WizardState } from './machine'
import { WIZARD_STEPS, wizardProgress, wizardStepNames } from './progress'

const error = new ApiError('unavailable', 'down')

describe('wizardProgress', () => {
  it.each<[WizardState, number]>([
    [{ step: 'pick_target', targets: [], selected: undefined, saving: false, error: undefined }, 0],
    [{ step: 'needs_manager' }, 1],
    [{ step: 'key_intro', failure: undefined, discardPrevious: false }, 1],
    [{ step: 'generating' }, 1],
    [{ step: 'show_key', recoveryKey: 'k' }, 1],
    [{ step: 'confirm', recoveryKey: 'k', groups: [0, 1], mismatch: false, submitting: false }, 1],
    [{ step: 'setup_uncertain', recoveryKey: 'k', error, checking: false }, 1],
    [
      {
        step: 'schedule',
        choice: DEFAULT_SCHEDULE,
        timezone: undefined,
        saving: false,
        error: undefined
      },
      2
    ],
    [{ step: 'done', status: status() }, WIZARD_STEPS]
  ])('places %o', (state, expected) => {
    expect(wizardProgress(state)).toBe(expected)
  })

  it.each<WizardState>([
    { step: 'loading' },
    { step: 'load_failed', error },
    { step: 'not_allowed' },
    { step: 'no_targets' },
    { step: 'already_protected' },
    { step: 'closed' }
  ])('shows no progress outside the sequence: %o', (state) => {
    expect(wizardProgress(state)).toBeUndefined()
  })
})

describe('wizardStepNames', () => {
  it('names one step per position', () => {
    expect(wizardStepNames((m) => m)).toEqual(['Destination', 'Recovery Key', 'Schedule'])
    expect(wizardStepNames((m) => m)).toHaveLength(WIZARD_STEPS)
  })
})
