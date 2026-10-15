// The bundle compares itself with the service it talks to (#102,
// decision #24). The service under test is real; its answer is altered in
// the browser to play a service of another release, which no fixture runs.

import { expect, test, type Page } from '@playwright/test'
import { API_LEVEL } from '../src/api/apilevel'
import { ADMIN, FAMILY } from './support/env'
import { apiJson, openVault, signIn } from './support/session'

interface ServiceVersion {
  version: string
  commit?: string
  api_level: number
}

/**
 * fakeServiceRelease makes GET /version answer as a service of another
 * release: the real answer with another version and API level.
 */
async function fakeServiceRelease(page: Page): Promise<void> {
  await page.route('**/backup/api/v1/version', async (route) => {
    const response = await route.fetch()
    const body = (await response.json()) as ServiceVersion
    await route.fulfill({
      response,
      json: { ...body, version: 'v99.0.0', api_level: body.api_level + 1 }
    })
  })
}

test('the service reports its build and the API level this bundle speaks', async ({ browser }) => {
  const { context, page } = await signIn(browser, FAMILY)
  await openVault(page, '/overview')

  const version = await apiJson<ServiceVersion>(page, '/version')
  expect(version.version).not.toBe('')
  expect(version.api_level).toBe(API_LEVEL)
  await expect(page.locator('[data-testid="version-mismatch"]')).toHaveCount(0)
  await context.close()
})

test('a member is told calmly to reload when the service speaks another API level', async ({
  browser
}) => {
  const { context, page } = await signIn(browser, FAMILY)
  await fakeServiceRelease(page)
  await openVault(page, '/overview')

  const notice = page.locator('[data-testid="version-mismatch"]')
  await expect(notice).toHaveAttribute('data-audience', 'member')
  await expect(notice).toHaveAttribute('data-tone', 'info')
  await expect(notice).toContainText('reload the page')
  await expect(notice).not.toContainText('v99.0.0')
  await context.close()
})

test('an administrator is warned, with both versions', async ({ browser }) => {
  const { context, page } = await signIn(browser, ADMIN)
  await fakeServiceRelease(page)
  await openVault(page, '/admin/targets')

  const notice = page.locator('[data-testid="version-mismatch"]')
  await expect(notice).toHaveAttribute('data-audience', 'admin')
  await expect(notice).toHaveAttribute('data-tone', 'warning')
  await expect(notice).toContainText('the backup service is v99.0.0')
  await expect(page.locator('[data-testid="versions"]')).toContainText('backup service v99.0.0')
  await context.close()
})
