// A family member protects their own Space, backs it up, gets it back, and
// opens the backup offline with the Recovery Key the browser made — before
// and after replacing that key (issue #42).
//
// Every request the browser sends during the whole journey is recorded, and
// the last test fails if any form of any Recovery Key shown here is in one.

import { expect, test, type Page } from '@playwright/test'
import { createHash, randomBytes } from 'node:crypto'
import { mkdirSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { decrypt, takeout } from './support/cli'
import { FAMILY, runContext } from './support/env'
import {
  KeyLeakRecorder,
  expectKeyNotStored,
  fillGate,
  findKeys,
  readRecoveryKey
} from './support/keys'
import { apiJson, openVault, signIn, type Session } from './support/session'
import { download, mkcol, personalDrive, upload, type Drive } from './support/webdav'

test.describe.configure({ mode: 'serial' })

// Shared along the journey, in memory only.
let session: Session
let page: Page
let recorder: KeyLeakRecorder
let drive: Drive
/** spaceId is the Space id the backup service uses (CS3 form). */
let spaceId: string
/** folder holds this run's files, so reruns never compare against old ones. */
let folder: string
const files: Record<string, Buffer> = {}
/** discardedKey was shown, then refused by the gate: it must open nothing. */
let discardedKey: string
let firstKey: string
let replacementKey: string
let firstBackupJob: string

interface Status {
  running: boolean
  last_run?: { id: string; state: string; error?: string }
  last_successful_run?: { id: string }
}

function spacePath(suffix = ''): string {
  return `/space/${encodeURIComponent(spaceId)}${suffix}`
}

async function status(): Promise<Status> {
  return apiJson<Status>(page, `/spaces/${encodeURIComponent(spaceId)}/backup/status`)
}

/** waitForBackupAfter waits for a successful run newer than `previous`. */
async function waitForBackupAfter(previous: string | undefined): Promise<string> {
  await expect
    .poll(
      async () => {
        const s = await status()
        if (s.last_run?.state === 'failed' && s.last_run.id !== previous) {
          throw new Error(`backup failed: ${s.last_run.error ?? 'no error recorded'}`)
        }
        return !s.running && s.last_successful_run && s.last_successful_run.id !== previous
      },
      { timeout: 180_000, intervals: [2000] }
    )
    .toBeTruthy()
  return (await status()).last_successful_run!.id
}

/** findFile finds a file by its relative path anywhere below `root`. */
function findFile(root: string, relative: string): string | undefined {
  const direct = join(root, relative)
  try {
    if (statSync(direct).isFile()) {
      return direct
    }
  } catch {
    // Not directly below root; the tool may add a level.
  }
  for (const entry of readdirSync(root)) {
    const full = join(root, entry)
    if (statSync(full).isDirectory()) {
      const found = findFile(full, relative)
      if (found) {
        return found
      }
    }
  }
  return undefined
}

function sha256(data: Buffer): string {
  return createHash('sha256').update(data).digest('hex')
}

/** expectTakeoutOpens restores a take-out with `key` and compares every file. */
function expectTakeoutOpens(takeoutDir: string, key: string, name: string, envelope?: string) {
  const out = join(runContext().runDir, `decrypted-${name}`)
  const result = decrypt(takeoutDir, out, key, envelope)
  expect(result.status, result.output).toBe(0)
  for (const [path, content] of Object.entries(files)) {
    const found = findFile(out, path)
    expect(found, `${path} in the decrypted take-out`).toBeDefined()
    expect(sha256(readFileSync(found!))).toBe(sha256(content))
  }
}

function expectTakeoutRefuses(takeoutDir: string, key: string, name: string, envelope?: string) {
  const result = decrypt(takeoutDir, join(runContext().runDir, `refused-${name}`), key, envelope)
  expect(result.status, result.output).not.toBe(0)
  expect(result.output).toMatch(/does not match/i)
}

function newTakeout(name: string): string {
  const dir = join(runContext().runDir, `takeout-${name}`)
  const result = takeout(spaceId, dir)
  expect(result.status, result.output).toBe(0)
  return dir
}

test.beforeAll(async ({ browser }) => {
  session = await signIn(browser, FAMILY)
  page = session.page
  recorder = new KeyLeakRecorder(session.context)
  drive = await personalDrive(FAMILY)

  folder = `E2E ${runContext().prefix.split('/')[1]}`
  files[`${folder}/Familienfoto.bin`] = randomBytes(256 * 1024)
  files[`${folder}/Notizen/Einkaufsliste – ä.txt`] = Buffer.from('Milch, Brot, Äpfel\n')
  await mkcol(FAMILY, drive, folder)
  await mkcol(FAMILY, drive, `${folder}/Notizen`)
  for (const [path, content] of Object.entries(files)) {
    await upload(FAMILY, drive, path, content)
  }
})

test.afterAll(async () => {
  await session?.context.close()
})

test('a Recovery Key typed back wrongly stops the setup', async () => {
  await openVault(page, '/overview')
  const card = page.locator('article', {
    has: page.getByRole('link', { name: drive.name, exact: true })
  })
  await card.locator('[data-testid="setup-action"]').click()

  // The service names a Space in CS3 form, `storage$space!opaque`. Graph
  // drops `!opaque` where it repeats the space part (measured in 8f), so the
  // two ids are the same Space but not the same string.
  await page.waitForURL(/\/backup-vault\/space\/[^/]+\/setup$/)
  spaceId = decodeURIComponent(page.url().split('/').at(-2)!)
  expect([drive.id, `${drive.id}!${drive.id.split('$')[1]}`]).toContain(spaceId)

  // One granted destination: bound without asking.
  await expect(page.locator('[data-step="key_intro"]')).toBeVisible()
  await page.locator('[data-testid="create-key"]').click()
  await expect(page.locator('[data-step="show_key"]')).toBeVisible({ timeout: 120_000 })
  discardedKey = await readRecoveryKey(page)

  await page.locator('[data-testid="key-saved"]').click()
  await expect(page.locator('[data-step="confirm"]')).toBeVisible()
  await fillGate(page, discardedKey, { wrong: true })
  await page.locator('[data-testid="gate-submit"]').click()

  await expect(page.locator('[data-testid="gate-mismatch"]')).toBeVisible()
  await expect(page.locator('[data-step="confirm"]')).toBeVisible()
  expect(recorder.posts('/backup/setup')).toHaveLength(0)
})

test('the family member protects their Space', async () => {
  // Starting over is what a person does after a failed gate. The wizard
  // resumes from the server's state, which has no keys yet.
  await page.reload()
  await expect(page.locator('[data-step="key_intro"]')).toBeVisible()
  await page.locator('[data-testid="create-key"]').click()
  await expect(page.locator('[data-step="show_key"]')).toBeVisible({ timeout: 120_000 })
  firstKey = await readRecoveryKey(page)
  expect(firstKey).not.toBe(discardedKey)

  await page.locator('[data-testid="key-saved"]').click()
  await fillGate(page, firstKey)
  await page.locator('[data-testid="gate-submit"]').click()

  await expect(page.locator('[data-step="schedule"]')).toBeVisible()
  expect(recorder.posts('/backup/setup')).toHaveLength(1)
  await page.locator('[data-testid="schedule-save"]').click()
  await expect(page.locator('[data-step="done"]')).toBeVisible()

  await expectKeyNotStored(page, [discardedKey, firstKey])
})

test('a backup runs and the Space shows as protected', async () => {
  await openVault(page, spacePath())
  const state = page.locator('[data-testid="state-label"]')
  await expect(state).toHaveAttribute('data-state', 'waiting')

  await page.locator('[data-testid="run-now"]').click()
  firstBackupJob = await waitForBackupAfter(undefined)

  // The board follows the run on its own and ends up green.
  await expect(state).toHaveAttribute('data-state', 'active', { timeout: 60_000 })
})

test('the backup comes back into a new folder, and the link opens it', async () => {
  await openVault(page, spacePath())
  await page.locator('[data-testid="restore-link"]').click()
  await expect(page.locator('[data-step="pick"]')).toBeVisible()
  await page.locator('input[name="snapshot"]').first().check()
  await page.locator('[data-testid="review"]').click()
  await expect(page.locator('[data-step="confirm"]')).toBeVisible()
  await page.locator('[data-testid="start-restore"]').click()

  await expect(page.locator('[data-step="succeeded"]')).toBeVisible({ timeout: 180_000 })
  const done = page.locator('[data-testid="done-folder"]')
  const restoreFolder = /Restore\/[^\s/]+/.exec(await done.innerText())?.[0]
  expect(restoreFolder, await done.innerText()).toBeDefined()

  for (const [path, content] of Object.entries(files)) {
    const res = await download(FAMILY, drive, `${restoreFolder}/${path}`)
    expect(res.status, `${restoreFolder}/${path}`).toBe(200)
    expect(sha256(res.body)).toBe(sha256(content))
  }

  // Not a plain path: the host resolved the Space, so this is a real link.
  const link = done.locator('[data-testid="folder-link"]')
  await expect(link).toBeVisible()
  await link.click()
  await page.waitForURL(/\/files\/spaces\//)
  await expect(page.getByText(folder, { exact: true }).first()).toBeVisible()
})

test('the browser-made Recovery Key opens a take-out with the offline tool', async () => {
  const dir = newTakeout('first')
  expectTakeoutOpens(dir, firstKey, 'first')
  // The key the gate refused was never taken into use.
  expectTakeoutRefuses(dir, discardedKey, 'discarded')
})

test('after replacing the Recovery Key, the new one opens the backups and the old one does not', async () => {
  await openVault(page, spacePath())
  await page.locator('[data-testid="recovery-key-link"]').click()
  await page.locator('[data-testid="replace"] a').click()

  await expect(page.locator('[data-step="enter_current"]')).toBeVisible()
  await page.locator('input[data-testid="current-key"]').fill(firstKey)
  await page.locator('[data-testid="current-submit"]').click()
  await expect(page.locator('[data-step="show_key"]')).toBeVisible({ timeout: 120_000 })
  replacementKey = await readRecoveryKey(page)
  expect(replacementKey).not.toBe(firstKey)
  await page.locator('[data-testid="key-saved"]').click()
  await fillGate(page, replacementKey)
  await page.locator('[data-testid="gate-submit"]').click()
  await expect(page.locator('[data-step="done"]')).toBeVisible()

  // Until the next backup, the bucket still holds the old envelope: a fresh
  // take-out opens with the old key and not with the new one (8d.4).
  const between = newTakeout('between')
  expectTakeoutOpens(between, firstKey, 'between-old')
  expectTakeoutRefuses(between, replacementKey, 'between-new')

  // The done screen's "Back up now" republishes the new envelope.
  await page.locator('[data-testid="back-up-now"]').click()
  await expect(page.locator('[data-testid="backup-started"]')).toBeVisible()
  await waitForBackupAfter(firstBackupJob)

  const after = newTakeout('after')
  expectTakeoutOpens(after, replacementKey, 'after-new')
  expectTakeoutRefuses(after, firstKey, 'after-old')

  // The key file from the Recovery Key page opens the take-out made before
  // the backup, with the new key only.
  await openVault(page, `${spacePath()}/recovery-key`)
  const downloading = page.waitForEvent('download')
  await page.locator('[data-testid="download-envelope"]').click()
  const file = await downloading
  expect(file.suggestedFilename()).toBe('recovery.ocbke')
  const envelopeDir = join(runContext().runDir, 'downloads')
  mkdirSync(envelopeDir, { recursive: true })
  const envelope = join(envelopeDir, 'recovery.ocbke')
  await file.saveAs(envelope)
  expectTakeoutOpens(between, replacementKey, 'between-envelope-new', envelope)
  expectTakeoutRefuses(between, firstKey, 'between-envelope-old', envelope)

  await expectKeyNotStored(page, [discardedKey, firstKey, replacementKey])
})

test('no Recovery Key ever left the browser', async () => {
  // Guards the guard: an empty recording would pass anything.
  expect(recorder.posts('/backup/setup')).toHaveLength(1)
  expect(recorder.posts('/backup/recovery-key/rotate')).toHaveLength(1)
  expect(recorder.posts('/backup/setup')[0]!.body).toContain('wrapped_dk_rk')
  const planted = { method: 'POST', url: 'https://x/', headers: {}, body: `{"k":"${firstKey}"}` }
  expect(findKeys([planted], [firstKey])).not.toEqual([])
  expect(recorder.requests.length).toBeGreaterThan(50)

  expect(recorder.leaks([discardedKey, firstKey, replacementKey])).toEqual([])
})
