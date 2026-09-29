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
export const FIXTURE_ORIGIN = 'https://localhost:9200'
export const API_BASE = `${FIXTURE_ORIGIN}/backup/api/v1`

/** BACKUPD_PORT is where the fixture's Caddy sends /backup/*. */
export const BACKUPD_PORT = 8080

export const ADMIN = { user: 'admin', password: 'admin' }
/** FAMILY is the "family member": a normal user with no admin role. */
export const FAMILY = { user: 'testuser', password: 'Test-User-1!' }

/** Garage is the dev Garage from docker-compose.dev.yml, as backupd reaches it. */
export const GARAGE = {
  endpoint: '127.0.0.1:3900',
  region: 'garage',
  bucket: 'opencloud-backup'
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
