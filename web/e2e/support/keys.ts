// Reading a Recovery Key off the page the way a person would, typing parts of
// it back into the gate, and proving it never left the browser.

import { expect, type BrowserContext, type Page, type Request } from '@playwright/test'
import { decodeRecoveryKey } from '../../src/crypto/recoverykey'

/**
 * readRecoveryKey reads the key off the page the three ways a person saves it
 * — as shown, selected by hand, and through "Copy" — and checks they are the
 * same text (#55). The key is shown whole, prefix included.
 */
export async function readRecoveryKey(page: Page): Promise<string> {
  const display = page.locator('[data-testid="recovery-key"]')
  await expect(display).toBeVisible()
  const shown = (await display.textContent()) ?? ''
  expect(groupsOf(shown), 'the shown key has seven groups').toHaveLength(7)
  decodeRecoveryKey(shown)

  await display.click({ clickCount: 3 })
  const selected = await page.evaluate(() => globalThis.getSelection()?.toString() ?? '')
  expect(selected, 'a hand selection is the shown key').toBe(shown)

  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.locator('[data-testid="copy-key"]').click()
  await expect(page.locator('[data-testid="copy-key"]')).toHaveText('Copied')
  const copied = await page.evaluate(() => globalThis.navigator.clipboard.readText())
  expect(copied, '"Copy" writes the shown key').toBe(shown)
  return shown
}

/** groupsOf returns the seven groups of a key, "group 1" first. */
export function groupsOf(key: string): string[] {
  return key.split('-').slice(1)
}

/**
 * fillGate types the groups the gate asks for. With `wrong`, the first one is
 * typed with its first symbol changed, which is the mistake a person makes.
 */
export async function fillGate(page: Page, key: string, options: { wrong?: boolean } = {}) {
  const groups = groupsOf(key)
  const inputs = page.locator('input[data-testid^="gate-"]')
  await expect(inputs.first()).toBeVisible()
  const count = await inputs.count()
  expect(count).toBe(2)
  for (let i = 0; i < count; i++) {
    const input = inputs.nth(i)
    const n = Number((await input.getAttribute('data-testid'))!.replace('gate-', ''))
    let value = groups[n - 1]!
    if (options.wrong && i === 0) {
      value = (value[0] === 'Z' ? 'Y' : 'Z') + value.slice(1)
    }
    await input.fill(value)
  }
}

/** RecordedRequest is what the browser sent, in full. */
export interface RecordedRequest {
  method: string
  url: string
  headers: Record<string, string>
  body: string
}

/**
 * KeyLeakRecorder keeps every request a browser context sends, to every
 * origin, and checks afterwards that no Recovery Key is in any of them.
 *
 * What counts as the key, beyond its display form: the bare symbols with or
 * without the prefix, lower case, the raw secret as base64, base64url and hex,
 * and each group on its own. Groups are checked in URLs and bodies only.
 * Headers carry long bearer tokens, where a five-symbol match by chance is
 * likely enough to make the test flaky, and a group in a header is not a leak
 * anyone would build by accident.
 */
export class KeyLeakRecorder {
  readonly requests: RecordedRequest[] = []

  constructor(context: BrowserContext) {
    context.on('request', (request) => this.requests.push(record(request)))
  }

  /** posts returns the POST requests to a path, for "nothing was sent" checks. */
  posts(pathSuffix: string): RecordedRequest[] {
    return this.requests.filter(
      (r) => r.method === 'POST' && new URL(r.url).pathname.endsWith(pathSuffix)
    )
  }

  /** leaks lists every place any form of any of `keys` was sent. Empty is the goal. */
  leaks(keys: string[]): string[] {
    return findKeys(this.requests, keys)
  }
}

/**
 * findKeys is the check itself, apart from any browser, so the journey can
 * show it finds a planted key before trusting it to find none.
 */
export function findKeys(requests: RecordedRequest[], keys: string[]): string[] {
  const found: string[] = []
  for (const key of keys) {
    const whole = wholeForms(key)
    const groups = groupsOf(key)
    for (const request of requests) {
      const where = `${request.method} ${request.url}`
      const headers = Object.values(request.headers).join('\n')
      for (const needle of whole) {
        if (request.url.includes(needle)) found.push(`key in URL: ${where}`)
        if (request.body.includes(needle)) found.push(`key in body: ${where}`)
        if (headers.includes(needle)) found.push(`key in headers: ${where}`)
      }
      for (const group of groups) {
        if (request.url.includes(group)) found.push(`key group in URL: ${where}`)
        if (request.body.includes(group)) found.push(`key group in body: ${where}`)
      }
    }
  }
  return [...new Set(found)]
}

/** expectKeyNotStored checks the page's web storage for any form of the keys. */
export async function expectKeyNotStored(page: Page, keys: string[]): Promise<void> {
  const stored = await page.evaluate(() => {
    const out: string[] = []
    for (const store of [localStorage, sessionStorage]) {
      for (let i = 0; i < store.length; i++) {
        const key = store.key(i) ?? ''
        out.push(key, store.getItem(key) ?? '')
      }
    }
    return out.join('\n')
  })
  for (const key of keys) {
    for (const needle of [...wholeForms(key), ...groupsOf(key)]) {
      expect(stored, 'a Recovery Key in browser storage').not.toContain(needle)
    }
  }
}

function wholeForms(key: string): string[] {
  const bare = groupsOf(key).join('')
  const secret = Buffer.from(decodeRecoveryKey(key))
  return [
    key,
    key.toLowerCase(),
    bare,
    bare.toLowerCase(),
    `ocbk1${bare}`,
    secret.toString('base64'),
    secret.toString('base64url'),
    secret.toString('hex')
  ]
}

function record(request: Request): RecordedRequest {
  return {
    method: request.method(),
    url: request.url(),
    headers: request.headers(),
    body: request.postDataBuffer()?.toString('latin1') ?? ''
  }
}
