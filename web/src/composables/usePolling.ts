// Poll while something is happening, and only then.
//
// Live progress is not tracked server-side (Phase 6 amendment), so a running
// job is watched by re-reading status. Two properties matter:
//
//   - **Ticks never overlap.** The next one is scheduled only after the
//     previous one settled, so a slow backend gets fewer requests, not a
//     queue of them.
//   - **Nothing outlives the view.** The timer is cleared when the owning
//     scope is disposed, so leaving the page stops the requests.

import { getCurrentScope, onScopeDispose } from 'vue'

/** PollingOptions tunes a poller. */
export interface PollingOptions {
  intervalMs: number
  /** shouldContinue is asked after every tick; false ends the polling. */
  shouldContinue: () => boolean
}

/** Poller is the handle a view holds. */
export interface Poller {
  /** start schedules the next tick unless one is already scheduled or running. */
  start: () => void
  /** stop cancels a scheduled tick. A tick already running finishes. */
  stop: () => void
  /** active reports whether a tick is scheduled or running. */
  active: () => boolean
}

/** usePolling runs tick every intervalMs while shouldContinue says so. */
export function usePolling(tick: () => Promise<void>, options: PollingOptions): Poller {
  let timer: ReturnType<typeof setTimeout> | undefined
  let running = false
  let stopped = false

  const schedule = () => {
    timer = setTimeout(async () => {
      timer = undefined
      running = true
      try {
        await tick()
      } catch {
        // The tick reports its own failures; a thrown one must not leave the
        // poller wedged in "running".
      } finally {
        running = false
      }
      if (!stopped && options.shouldContinue()) {
        schedule()
      }
    }, options.intervalMs)
  }

  const stop = () => {
    stopped = true
    if (timer !== undefined) {
      clearTimeout(timer)
      timer = undefined
    }
  }

  const start = () => {
    stopped = false
    if (timer === undefined && !running) {
      schedule()
    }
  }

  if (getCurrentScope()) {
    onScopeDispose(stop)
  }

  return { start, stop, active: () => timer !== undefined || running }
}
