// Where things are, for the end-to-end run.
//
// Nothing secret is written here. The fixture users' passwords are the
// fixture's own (seed.sh). The Garage key pair is read from the dev compose
// file, so it is not written into a second place. Wrap keys are generated
// per run (global-setup.ts).

import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

export const REPO_ROOT = resolve(import.meta.dirname, '..', '..', '..')
export const FIXTURE_DIR = join(REPO_ROOT, 'test', 'fixtures', 'opencloud')
/** FIXTURE_ORIGIN is the fixture's OpenCloud (test/fixtures/opencloud/lib.sh). */
export const FIXTURE_ORIGIN = 'https://cloud.opencloud.localhost:9200'
export const API_BASE = `${FIXTURE_ORIGIN}/backup/api/v1`

/** BACKUPD_PORT is where the fixture sends /backup/api in host mode. */
export const BACKUPD_PORT = 8080

export const ADMIN = { user: 'admin', password: 'admin' }
/** FAMILY is the "family member": a normal user with no admin role. */
export const FAMILY = { user: 'testuser', password: 'Test-User-1!' }

/**
 * Garage is the dev Garage from docker-compose.dev.yml. The name reaches it
 * from the host (any *.localhost is loopback) and from the add-on's backupd
 * container (test/fixtures/opencloud/fixture.yml), so the destination the
 * admin creates works for the service and for the take-out tool alike.
 */
export const GARAGE = {
  endpoint: 'garage.opencloud.localhost:3900',
  region: 'garage',
  bucket: 'opencloud-backup'
}

/**
 * BackupdMode is where the fixture runs backupd (up.sh, FIXTURE_BACKUPD):
 * on the host, started by global-setup.ts, or as the add-on's container.
 */
export type BackupdMode = 'host' | 'container'

export function backupdMode(): BackupdMode {
  const mode = fixtureEnv().FIXTURE_BACKUPD ?? 'host'
  if (mode !== 'host' && mode !== 'container') {
    throw new Error(`fixture.env: FIXTURE_BACKUPD is '${mode}', want host or container`)
  }
  return mode
}

/**
 * fixtureEnv reads the `export KEY=value` lines up.sh and seed.sh write.
 * Values may be single-quoted; nothing else in that file needs handling.
 */
export function fixtureEnv(): Record<string, string> {
  const env: Record<string, string> = {}
  const text = readFileSync(join(FIXTURE_DIR, 'fixture.env'), 'utf8')
  for (const line of text.split('\n')) {
    const match = /^export\s+([A-Z0-9_]+)=(.*)$/.exec(line.trim())
    if (!match) {
      continue
    }
    const raw = match[2]!
    env[match[1]!] = raw.startsWith("'") && raw.endsWith("'") ? raw.slice(1, -1) : raw
  }
  return env
}

/** garageKeys reads the dev Garage's default key pair from its compose file. */
export function garageKeys(): { accessKeyId: string; secretAccessKey: string } {
  const compose = readFileSync(join(REPO_ROOT, 'docker-compose.dev.yml'), 'utf8')
  const read = (name: string): string => {
    const match = new RegExp(`${name}:\\s*"([^"]+)"`).exec(compose)
    if (!match) {
      throw new Error(`docker-compose.dev.yml has no ${name}`)
    }
    return match[1]!
  }
  return {
    accessKeyId: read('GARAGE_DEFAULT_ACCESS_KEY'),
    secretAccessKey: read('GARAGE_DEFAULT_SECRET_KEY')
  }
}

/** RunContext is what global-setup.ts hands to the specs through the environment. */
export interface RunContext {
  /** runDir holds this run's binaries, logs, take-outs and downloads. */
  runDir: string
  binDir: string
  /** prefix is this run's repository prefix in the bucket. */
  prefix: string
}

export function runContext(): RunContext {
  const runDir = process.env.E2E_RUN_DIR
  const prefix = process.env.E2E_PREFIX
  if (!runDir || !prefix) {
    throw new Error('E2E_RUN_DIR / E2E_PREFIX unset: run through playwright.config.ts')
  }
  return { runDir, binDir: join(runDir, 'bin'), prefix }
}
