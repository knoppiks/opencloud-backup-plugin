import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import { usePolling } from './usePolling'

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())

describe('usePolling', () => {
  it('ticks until told to stop continuing', async () => {
    let remaining = 3
    const tick = vi.fn(async () => {
      remaining--
    })
    const poller = usePolling(tick, { intervalMs: 1000, shouldContinue: () => remaining > 0 })

    poller.start()
    await vi.advanceTimersByTimeAsync(10_000)

    expect(tick).toHaveBeenCalledTimes(3)
    expect(poller.active()).toBe(false)
  })

  // A slow backend must get fewer requests, not a queue of them.
  it('never overlaps ticks', async () => {
    let inFlight = 0
    let maxInFlight = 0
    const tick = vi.fn(async () => {
      inFlight++
      maxInFlight = Math.max(maxInFlight, inFlight)
      await new Promise((resolve) => setTimeout(resolve, 5000))
      inFlight--
    })
    const poller = usePolling(tick, { intervalMs: 1000, shouldContinue: () => true })

    poller.start()
    poller.start() // a second start while scheduled is a no-op
    await vi.advanceTimersByTimeAsync(20_000)
    poller.stop()

    expect(maxInFlight).toBe(1)
    // 1s wait + 5s tick per cycle.
    expect(tick.mock.calls.length).toBeLessThanOrEqual(4)
  })

  it('keeps going after a tick throws', async () => {
    let calls = 0
    const tick = vi.fn(async () => {
      calls++
      if (calls === 1) {
        throw new Error('boom')
      }
    })
    const poller = usePolling(tick, { intervalMs: 1000, shouldContinue: () => calls < 2 })

    poller.start()
    await vi.advanceTimersByTimeAsync(5000)
    expect(tick).toHaveBeenCalledTimes(2)
  })

  // Leaving the page must stop the requests.
  it('stops when its scope is disposed', async () => {
    const tick = vi.fn(async () => {})
    const scope = effectScope()
    const poller = scope.run(() =>
      usePolling(tick, { intervalMs: 1000, shouldContinue: () => true })
    )!

    poller.start()
    await vi.advanceTimersByTimeAsync(2500)
    const before = tick.mock.calls.length
    scope.stop()
    await vi.advanceTimersByTimeAsync(10_000)

    expect(before).toBe(2)
    expect(tick).toHaveBeenCalledTimes(before)
    expect(poller.active()).toBe(false)
  })

  it('can be restarted after stopping', async () => {
    const tick = vi.fn(async () => {})
    const poller = usePolling(tick, { intervalMs: 1000, shouldContinue: () => false })

    poller.start()
    await vi.advanceTimersByTimeAsync(1000)
    poller.start()
    await vi.advanceTimersByTimeAsync(1000)
    expect(tick).toHaveBeenCalledTimes(2)
  })
})
