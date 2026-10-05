// How serious something is, drawn the same way everywhere (Phase 8g).
//
// A tone picks a colour pair from the host theme's roles, so light and dark
// theme both work and nothing hard-codes a colour. Colour is never the only
// cue: every tagged or announced thing also has an icon and words.
//
// The design system's tag only knows primary/secondary/tertiary and its
// notification is a dismissable toast, so status tags and notices use these
// classes instead, shaped like the host's own tag.

import type { SpaceState } from '../status/spacestate'

/** Tone is the seriousness of a state or a notice. */
export type Tone = 'success' | 'info' | 'neutral' | 'warning' | 'danger'

/** toneClasses are the background and text roles of a tone. */
export function toneClasses(tone: Tone): string {
  switch (tone) {
    case 'success':
      return 'ext:bg-role-primary-container ext:text-role-on-primary-container'
    case 'info':
      return 'ext:bg-role-secondary-container ext:text-role-on-secondary-container'
    case 'neutral':
      return 'ext:bg-role-surface-container-high ext:text-role-on-surface'
    case 'warning':
      return 'ext:bg-role-tertiary-container ext:text-role-on-tertiary-container'
    case 'danger':
      return 'ext:bg-role-error-container ext:text-role-on-error-container'
  }
}

/** toneIcon is the icon a notice of that tone leads with. */
export function toneIcon(tone: Tone): string {
  switch (tone) {
    case 'success':
      return 'checkbox-circle'
    case 'info':
    case 'neutral':
      return 'information'
    case 'warning':
      return 'alert'
    case 'danger':
      return 'error-warning'
  }
}

/** StateLook is how a Space's state is tagged. */
export interface StateLook {
  tone: Tone
  icon: string
}

/**
 * stateLook maps a Space's state to its tag. The tones follow needsAttention:
 * a failure is danger, the two "act on this" states are warnings, and only a
 * Space whose last backup succeeded is success.
 */
export function stateLook(state: SpaceState): StateLook {
  switch (state) {
    case 'active':
      return { tone: 'success', icon: 'shield-check' }
    case 'running':
      return { tone: 'info', icon: 'loader-4' }
    case 'waiting':
      return { tone: 'neutral', icon: 'time' }
    case 'paused':
      return { tone: 'neutral', icon: 'pause-circle' }
    case 'not_set_up':
      return { tone: 'neutral', icon: 'shield-cross' }
    case 'setup_incomplete':
      return { tone: 'warning', icon: 'alert' }
    case 'stale':
      return { tone: 'warning', icon: 'alert' }
    case 'failed':
      return { tone: 'danger', icon: 'error-warning' }
  }
}
