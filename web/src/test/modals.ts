// A stand-in for web-pkg's modal store, for component tests.
//
// The host draws the dialog; what a component decides is what it dispatches.
// So the double records every dispatched modal, and a spec presses its
// buttons by calling `onConfirm`/`onCancel` itself.

import type { Modal } from '@opencloud-eu/web-pkg'

/** ModalsDouble is the fake store plus what was dispatched to it. */
export interface ModalsDouble {
  dispatched: Omit<Modal, 'id'>[]
  store: { dispatchModal: (modal: Omit<Modal, 'id'>) => Modal }
  /** last is the most recently dispatched modal; throws when there is none. */
  last: () => Omit<Modal, 'id'>
}

/** modalsDouble creates an empty fake modal store. */
export function modalsDouble(): ModalsDouble {
  const dispatched: Omit<Modal, 'id'>[] = []
  return {
    dispatched,
    store: {
      dispatchModal: (modal) => {
        dispatched.push(modal)
        return { ...modal, id: `modal-${dispatched.length}` }
      }
    },
    last: () => {
      const modal = dispatched.at(-1)
      if (!modal) {
        throw new Error('no modal was dispatched')
      }
      return modal
    }
  }
}
