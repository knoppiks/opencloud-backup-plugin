// Signing in the way a person does: through OpenCloud's own login page, with
// the real OIDC flow, so the token the extension forwards is a real one.

import { expect, type Browser, type BrowserContext, type Page } from '@playwright/test'

export interface Session {
  context: BrowserContext
  page: Page
}

export async function signIn(
  browser: Browser,
  account: { user: string; password: string }
): Promise<Session> {
  const context = await browser.newContext()
  const page = await context.newPage()
  await page.goto('/')
  const user = page.locator('#oc-login-username, input[name="username"]').first()
  await user.waitFor({ timeout: 60_000 })
  await user.fill(account.user)
  await page.locator('#oc-login-password, input[name="password"]').first().fill(account.password)
  await page.keyboard.press('Enter')
  await page.waitForURL(/\/files\//, { timeout: 60_000 })
  return { context, page }
}

/** openVault opens a Backup Vault route and waits for the page to render. */
export async function openVault(page: Page, path: string): Promise<void> {
  await page.goto(`/backup-vault${path}`)
  await expect(page.locator('main h1').first()).toBeVisible()
}

/**
 * accessToken reads the session's OIDC token, so a test can ask the API
 * directly what the UI does not show (a status code, a stored grant).
 */
export async function accessToken(page: Page): Promise<string> {
  const token = await page.evaluate(() => {
    for (const store of [sessionStorage, localStorage]) {
      for (let i = 0; i < store.length; i++) {
        const key = store.key(i)
        if (key?.startsWith('oc_oAuth.user:') || key?.startsWith('oidc.user:')) {
          return (JSON.parse(store.getItem(key) ?? '{}') as { access_token?: string }).access_token
        }
      }
    }
    return undefined
  })
  if (!token) {
    throw new Error('no OIDC session found in the page')
  }
  return token
}

/** apiJson reads a backup API document with the page's own token. */
export async function apiJson<T>(page: Page, path: string): Promise<T> {
  const token = await accessToken(page)
  return page.evaluate(
    async ([url, bearer]) => {
      const res = await fetch(url, { headers: { Authorization: `Bearer ${bearer}` } })
      if (!res.ok) {
        throw new Error(`${url}: ${res.status}`)
      }
      return res.json()
    },
    [`/backup/api/v1${path}`, token] as const
  ) as Promise<T>
}

/** apiStatus asks the backup API with the page's own token and returns the status. */
export async function apiStatus(page: Page, path: string): Promise<number> {
  const token = await accessToken(page)
  return page.evaluate(
    async ([url, bearer]) =>
      (await fetch(url, { headers: { Authorization: `Bearer ${bearer}` } })).status,
    [`/backup/api/v1${path}`, token] as const
  )
}
