// Saving bytes as a file, without a round trip.
//
// The envelope is already in the page (it came from the GET the check uses),
// so the download is built here: a Blob and an object URL, both local, and
// nothing sent anywhere (8d.4 decision 4).

/**
 * REVOKE_AFTER_MS is how long the object URL outlives the click. Some browsers
 * start reading the Blob after click() returns, and fail the download when the
 * URL is already gone. The Blob is ciphertext any member may read, so a short
 * delay costs nothing.
 */
export const REVOKE_AFTER_MS = 30_000

/** DomLike is the slice of the browser saveBytes uses; injected for tests. */
export interface DomLike {
  document: Pick<Document, 'createElement' | 'body'>
  URL: Pick<typeof URL, 'createObjectURL' | 'revokeObjectURL'>
  later: (fn: () => void, ms: number) => void
}

function browser(): DomLike {
  return {
    document: globalThis.document,
    URL: globalThis.URL,
    later: (fn, ms) => {
      globalThis.setTimeout(fn, ms)
    }
  }
}

/** saveBytes offers bytes to the user as a download named fileName. */
export function saveBytes(bytes: Uint8Array, fileName: string, dom: DomLike = browser()): void {
  const blob = new Blob([Uint8Array.from(bytes)], { type: 'application/octet-stream' })
  const url = dom.URL.createObjectURL(blob)
  const link = dom.document.createElement('a')
  try {
    link.href = url
    link.download = fileName
    link.rel = 'noopener'
    link.style.display = 'none'
    dom.document.body.appendChild(link)
    link.click()
  } finally {
    link.remove()
    dom.later(() => dom.URL.revokeObjectURL(url), REVOKE_AFTER_MS)
  }
}
