// An administrator sets up the backup destination through Backup Vault and
// grants it to one family member; the family member cannot reach any of it
// (8f decision 5). This is also the in-browser proof of 8e's exit criterion:
// admin sees the view, a non-admin does not, and the server answers 403.

import { expect, test, type Response } from '@playwright/test'
import { ADMIN, FAMILY, GARAGE, fixtureEnv, garageKeys, runContext } from './support/env'
import { apiJson, apiStatus, openVault, signIn } from './support/session'

test.describe.configure({ mode: 'serial' })

test('an administrator creates the destination and grants it to one person', async ({
  browser
}) => {
  const { prefix } = runContext()
  const keys = garageKeys()
  const { context, page } = await signIn(browser, ADMIN)

  // Credentials are write-only: no answer from the backup API may carry one.
  const apiBodies: Promise<string>[] = []
  page.on('response', (response: Response) => {
    if (new URL(response.url()).pathname.startsWith('/backup/api/')) {
      apiBodies.push(response.text().catch(() => ''))
    }
  })

  await openVault(page, '/overview')
  // Offered in the host's left navigation, to admins only (8g).
  await page.getByRole('link', { name: 'Backup destinations', exact: true }).click()
  await expect(page.locator('main h1')).toHaveText('Backup destinations')
  await page.locator('[data-testid="add"]').click()

  await page.locator('input[data-testid="name"]').fill('Family backup')
  await page.locator('input[data-testid="endpoint"]').fill(GARAGE.endpoint)
  await page.locator('input[data-testid="bucket"]').fill(GARAGE.bucket)
  await page.locator('input[data-testid="region"]').fill(GARAGE.region)
  await page.locator('input[data-testid="prefix"]').fill(prefix)
  // The host's checkbox carries the test id on its wrapper (8g).
  await page.locator('[data-testid="path-style"] input').check()
  await page.locator('[data-testid="disable-tls"] input').check()
  const backupKeys = page.locator('[data-testid="backup-keys"]')
  await backupKeys.locator('input[data-testid="access-key-id"]').fill(keys.accessKeyId)
  await backupKeys.locator('input[data-testid="secret-access-key"]').fill(keys.secretAccessKey)

  await page.locator('[data-testid="check"]').click()
  await expect(
    page.locator('[data-testid="check-results"] li[data-role="backup"]')
  ).toHaveAttribute('data-outcome', 'ok')

  await page.locator('[data-testid="save"]').click()
  await page.waitForURL(/\/backup-vault\/admin\/targets\/[0-9a-f]{32}$/)
  const targetId = page.url().split('/').pop()!

  // Nothing about the keys is on the page, now or after a reload.
  await expect(page.locator('[data-testid="nobody"]')).toBeVisible()
  await expect(page.locator('input[type="password"]')).toHaveCount(0)

  await page.locator('input[data-testid="user-search"]').fill(FAMILY.user)
  const match = page.locator('[data-testid="matches"] li', { hasText: 'Test User' })
  await match.locator('[data-testid="add-user"]').click()
  await page.locator('[data-testid="save-audience"]').click()
  await expect(page.locator('[data-testid="audience-saved"]')).toBeVisible()

  // The grant stores the OpenCloud user id, which is what the service
  // authorizes on (decisions.md, amendments before the first deployment).
  const { grants } = await apiJson<{ grants: { scope: string; user_id?: string }[] }>(
    page,
    `/admin/targets/${targetId}/grants`
  )
  expect(grants).toEqual([{ scope: 'user', user_id: fixtureEnv().OC_NORMAL_USER_ID }])

  await page.reload()
  await expect(page.locator('[data-testid="people"]')).toContainText('Test User')
  const html = await page.content()
  expect(html).not.toContain(keys.secretAccessKey)
  expect(html).not.toContain(keys.accessKeyId)

  for (const body of await Promise.all(apiBodies)) {
    expect(body).not.toContain(keys.secretAccessKey)
    expect(body).not.toContain(keys.accessKeyId)
  }
  await context.close()
})

test('the family member sees the destination and none of its administration', async ({
  browser
}) => {
  const { context, page } = await signIn(browser, FAMILY)

  await openVault(page, '/overview')
  await expect(page.locator('[data-testid="no-targets"]')).toHaveCount(0)
  await expect(page.getByRole('link', { name: 'Backup destinations', exact: true })).toHaveCount(0)

  const { targets } = await apiJson<{ targets: { name: string }[] }>(page, '/targets')
  expect(targets.map((t) => t.name)).toEqual(['Family backup'])

  // Typed in by hand: the client gate shows a notice and calls nothing, and
  // the server refuses anyway.
  await openVault(page, '/admin/targets')
  await expect(page.locator('[data-testid="not-admin"]')).toBeVisible()
  await expect(page.locator('[data-testid="destinations"]')).toHaveCount(0)
  expect(await apiStatus(page, '/admin/targets')).toBe(403)

  await context.close()
})

test('an administrator deletes a destination through the host’s dialog', async ({ browser }) => {
  const { prefix } = runContext()
  const keys = garageKeys()
  const { context, page } = await signIn(browser, ADMIN)

  // A destination of its own, granted to nobody, so the journey's is untouched.
  await openVault(page, '/admin/targets/new')
  await page.locator('input[data-testid="name"]').fill('Throwaway')
  await page.locator('input[data-testid="endpoint"]').fill(GARAGE.endpoint)
  await page.locator('input[data-testid="bucket"]').fill(GARAGE.bucket)
  await page.locator('input[data-testid="prefix"]').fill(`${prefix}throwaway/`)
  const backupKeys = page.locator('[data-testid="backup-keys"]')
  await backupKeys.locator('input[data-testid="access-key-id"]').fill(keys.accessKeyId)
  await backupKeys.locator('input[data-testid="secret-access-key"]').fill(keys.secretAccessKey)
  await page.locator('[data-testid="save"]').click()
  await page.waitForURL(/\/backup-vault\/admin\/targets\/[0-9a-f]{32}$/)
  const targetId = page.url().split('/').pop()!

  // Cancelling the dialog deletes nothing.
  await page.locator('[data-testid="delete"]').click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('Delete “Throwaway”?')
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).toHaveCount(0)
  expect(await apiStatus(page, `/admin/targets/${targetId}`)).toBe(200)

  await page.locator('[data-testid="delete"]').click()
  await page.getByRole('dialog').locator('.oc-modal-body-actions-confirm').click()
  await page.waitForURL(/\/backup-vault\/admin\/targets$/)
  await expect(page.getByRole('dialog')).toHaveCount(0)
  expect(await apiStatus(page, `/admin/targets/${targetId}`)).toBe(404)
  await expect(page.locator('[data-testid="destinations"]')).not.toContainText('Throwaway')

  await context.close()
})
