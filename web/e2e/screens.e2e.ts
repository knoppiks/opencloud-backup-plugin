// The screenshot tour (Phase 8g, issue #59): every Backup Vault page as the
// family member and as the administrator, in light and dark theme, plus the
// Files app as the reference the vault should look like.
//
// It asserts nothing about pixels. The PNGs are the record a reviewer
// compares against Files; CI uploads them as the `ui-screens` artifact.
//
// It runs after the journey, so the family Space is set up and has history,
// and the admin's destination exists. Every page is only opened: nothing
// here starts a run or changes a setting.

import { expect, test, type Page } from '@playwright/test'
import { join } from 'node:path'
import { ADMIN, FAMILY } from './support/env'
import { apiJson, openVault, signIn } from './support/session'

test.describe.configure({ mode: 'serial' })

const THEMES = ['light', 'dark'] as const
/**
 * VIEWPORT is a desktop width and tall enough for the longest page: the
 * host scrolls inside its own container, so `fullPage` cannot extend it.
 */
const VIEWPORT = { width: 1440, height: 1400 }

interface Space {
  id: string
  type: string
}

/** screensDir is where the PNGs go; CI uploads this directory. */
function screensDir(): string {
  return join(test.info().project.outputDir, 'screens')
}

/** personalSpacePath is the vault route of the signed-in user's own Space. */
async function personalSpacePath(page: Page): Promise<string> {
  const { spaces } = await apiJson<{ spaces: Space[] }>(page, '/spaces')
  const personal = spaces.find((s) => s.type === 'personal')
  if (!personal) {
    throw new Error('the signed-in user has no personal Space')
  }
  return `/space/${encodeURIComponent(personal.id)}`
}

/**
 * settled waits until nothing on the page is loading any more. Not
 * `networkidle`: OpenCloud keeps a server-sent-events stream open.
 */
async function settled(page: Page): Promise<void> {
  await expect(page.locator('main').first()).toBeVisible()
  await expect(page.locator('.oc-spinner:visible')).toHaveCount(0)
}

/** shoot opens a vault route, waits for its data, and saves a full-page PNG. */
async function shoot(page: Page, name: string, path: string): Promise<void> {
  await openVault(page, path)
  await settled(page)
  await page.screenshot({ path: join(screensDir(), `${name}.png`), fullPage: true })
}

for (const theme of THEMES) {
  test(`family member pages (${theme})`, async ({ browser }) => {
    const { context, page } = await signIn(browser, FAMILY, {
      colorScheme: theme,
      viewport: VIEWPORT
    })
    const prefix = `${theme}-family`

    await settled(page)
    await page.screenshot({ path: join(screensDir(), `${prefix}-files-reference.png`) })

    const space = await personalSpacePath(page)
    await shoot(page, `${prefix}-overview`, '/overview')
    await shoot(page, `${prefix}-space`, space)
    await shoot(page, `${prefix}-setup`, `${space}/setup`)
    await shoot(page, `${prefix}-restore`, `${space}/restore`)
    await shoot(page, `${prefix}-recovery-key`, `${space}/recovery-key`)
    await shoot(page, `${prefix}-recovery-key-replace`, `${space}/recovery-key/replace`)
    await shoot(page, `${prefix}-admin-refused`, '/admin/targets')

    await context.close()
  })

  test(`administrator pages (${theme})`, async ({ browser }) => {
    const { context, page } = await signIn(browser, ADMIN, {
      colorScheme: theme,
      viewport: VIEWPORT
    })
    const prefix = `${theme}-admin`

    const space = await personalSpacePath(page)
    await shoot(page, `${prefix}-overview`, '/overview')
    await shoot(page, `${prefix}-space-not-set-up`, space)
    await shoot(page, `${prefix}-setup-no-destination`, `${space}/setup`)
    await shoot(page, `${prefix}-destinations`, '/admin/targets')
    await shoot(page, `${prefix}-destination-new`, '/admin/targets/new')

    const { targets } = await apiJson<{ targets: { id: string }[] }>(page, '/admin/targets')
    if (targets[0]) {
      await shoot(page, `${prefix}-destination-edit`, `/admin/targets/${targets[0].id}`)
    }

    await context.close()
  })
}
