// Where the setup wizard is, as "step n of 3" (Phase 8g).
//
// The steps are always the same three, also when the destination was chosen
// without asking because only one is granted: then the first step simply
// shows as done. A count that changed with the number of destinations would
// make "step 2" mean different things to different people.
//
// Screens outside the sequence (a viewer, no destination, already protected)
// show no progress at all: there is nothing to make progress on.

import type { Gettext } from '../api/errortext'
import type { WizardState } from './machine'

/** WIZARD_STEPS is the number of steps the indicator shows. */
export const WIZARD_STEPS = 3

/**
 * wizardProgress is the 0-based step the wizard is on, WIZARD_STEPS once it
 * is done, or undefined for a screen outside the sequence.
 */
export function wizardProgress(state: WizardState): number | undefined {
  switch (state.step) {
    case 'pick_target':
      return 0
    case 'needs_manager':
    case 'key_intro':
    case 'generating':
    case 'show_key':
    case 'confirm':
    case 'setup_uncertain':
      return 1
    case 'schedule':
      return 2
    case 'done':
      return WIZARD_STEPS
    default:
      return undefined
  }
}

/** wizardStepNames names the steps, in order. */
export function wizardStepNames($gettext: Gettext): string[] {
  return [$gettext('Destination'), $gettext('Recovery Key'), $gettext('Schedule')]
}
