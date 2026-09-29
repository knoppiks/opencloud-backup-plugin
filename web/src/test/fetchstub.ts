// A recording fetch for API client specs.

import { vi } from 'vitest'

/** FetchCall is one recorded request. */
export interface FetchCall {
  url: string
  init: RequestInit
}

/** stubFetch builds a fetch that answers every request with the given status and body. */
export function stubFetch(status: number, body?: unknown, contentType = 'application/json') {
  const calls: FetchCall[] = []
  const fetchImpl = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
    calls.push({ url: String(url), init: init ?? {} })
    const text = body === undefined ? '' : typeof body === 'string' ? body : JSON.stringify(body)
    return new Response(text === '' ? null : text, {
      status,
      headers: { 'Content-Type': contentType }
    })
  })
  return { calls, fetchImpl: fetchImpl as unknown as typeof fetch }
}
