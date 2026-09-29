// Putting files into a Space and reading them back, as the user, outside the
// browser. The fixture enables basic auth for exactly this (seed.sh).

import { expect } from '@playwright/test'
import { FIXTURE_ORIGIN } from './env'
import { http } from './http'

type Account = { user: string; password: string }

export interface Drive {
  /** id is the graph drive id, which is also the CS3 Space id the service uses. */
  id: string
  name: string
}

/** personalDrive returns the user's personal Space. */
export async function personalDrive(account: Account): Promise<Drive> {
  const res = await http(`${FIXTURE_ORIGIN}/graph/v1.0/me/drive`, {
    basicAuth: [account.user, account.password]
  })
  expect(res.status, res.text()).toBe(200)
  const drive = JSON.parse(res.text()) as { id: string; name: string }
  return { id: drive.id, name: drive.name }
}

function davUrl(drive: Drive, path: string): string {
  const encoded = path
    .split('/')
    .filter(Boolean)
    .map((segment) => encodeURIComponent(segment))
    .join('/')
  return `${FIXTURE_ORIGIN}/dav/spaces/${encodeURIComponent(drive.id)}/${encoded}`
}

export async function mkcol(account: Account, drive: Drive, path: string): Promise<void> {
  const res = await http(davUrl(drive, path), {
    method: 'MKCOL',
    basicAuth: [account.user, account.password]
  })
  // 405: already there, which a second run finds.
  expect([201, 405], res.text()).toContain(res.status)
}

/**
 * upload writes a file and waits until it can be read back.
 *
 * The wait is #37: right after an upload OpenCloud answers 425 while it
 * post-processes, and a backup started in that window fails. Until the
 * service retries on its own, the test waits for what a person would never
 * notice.
 */
export async function upload(
  account: Account,
  drive: Drive,
  path: string,
  content: Buffer
): Promise<void> {
  const res = await http(davUrl(drive, path), {
    method: 'PUT',
    body: content,
    basicAuth: [account.user, account.password]
  })
  expect([201, 204], res.text()).toContain(res.status)
  await expect
    .poll(async () => (await download(account, drive, path)).status, { timeout: 60_000 })
    .toBe(200)
}

export function download(account: Account, drive: Drive, path: string) {
  return http(davUrl(drive, path), { basicAuth: [account.user, account.password] })
}
