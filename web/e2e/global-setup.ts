// Brings up what the fixture does not: backupd itself (8f decision 3).
//
// Checks the preconditions first and says which one is missing, then builds
// backupd, takeout and decrypt, and starts backupd on the host where the
// fixture's Caddy expects it. The returned function is the teardown.
//
// Per run, fresh:
//   - SRW and TW keys, generated here and handed to backupd's environment
//     only. They are never written to disk or logged.
//   - State, in memory (8f decision 4; switch to a CS3 state Space once #37
//     is fixed).
//   - A repository prefix in the bucket. Key setup is once-only per Space and
//     the bucket outlives the run, so a second run must not find the first
//     run's repository.

import { spawn, spawnSync, type ChildProcess } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, openSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  BACKUPD_PORT,
  FIXTURE_DIR,
  FIXTURE_ORIGIN,
  GARAGE,
  REPO_ROOT,
  fixtureEnv
} from './support/env'
import { http } from './support/http'

const READY_TIMEOUT_MS = 60_000

export default async function globalSetup(): Promise<() => Promise<void>> {
  await checkPreconditions()

  // E2E_RUN_ROOT lets CI keep the run directory somewhere it can upload
  // backupd.log from after a failure.
  const runRoot = process.env.E2E_RUN_ROOT ?? tmpdir()
  mkdirSync(runRoot, { recursive: true })
  const runDir = mkdtempSync(join(runRoot, 'ocbp-e2e-'))
  const binDir = join(runDir, 'bin')
  mkdirSync(binDir)
  buildBinaries(binDir)

  const stamp = new Date().toISOString().replace(/[:.]/g, '-')
  const prefix = `e2e/${stamp}-${randomBytes(3).toString('hex')}/`

  const backupd = startBackupd(binDir, runDir)
  await waitForReady(backupd, runDir)

  process.env.E2E_RUN_DIR = runDir
  process.env.E2E_PREFIX = prefix
  console.log(`e2e: run directory ${runDir}, repository prefix ${prefix}`)

  return () => stop(backupd)
}

async function checkPreconditions(): Promise<void> {
  for (const file of ['fixture.env', 'ca.crt']) {
    if (!existsSync(join(FIXTURE_DIR, file))) {
      throw new Error(`no ${file} in ${FIXTURE_DIR}: run 'make dev-up' first`)
    }
  }

  const config = await http(`${FIXTURE_ORIGIN}/config.json`).catch(() => undefined)
  if (config?.status !== 200) {
    throw new Error(`the fixture does not answer on ${FIXTURE_ORIGIN}: run 'make dev-up'`)
  }
  if (!config.text().includes('backup-vault')) {
    throw new Error("Backup Vault is not installed in the fixture: run 'make web-install-fixture'")
  }

  const garage = await http(`http://${GARAGE.endpoint}/`).catch(() => undefined)
  if (garage === undefined) {
    throw new Error(`the dev Garage does not answer on ${GARAGE.endpoint}: run 'make dev-up'`)
  }

  const occupied = await http(`http://127.0.0.1:${BACKUPD_PORT}/healthz`, {
    timeoutMs: 2000
  }).catch(() => undefined)
  if (occupied !== undefined) {
    throw new Error(
      `something already answers on :${BACKUPD_PORT} (a backupd started by hand?). ` +
        'The E2E run starts its own; stop the other one first.'
    )
  }
}

function buildBinaries(binDir: string): void {
  const result = spawnSync(
    'go',
    ['build', '-o', `${binDir}/`, './cmd/backupd', './cmd/takeout', './cmd/decrypt'],
    { cwd: REPO_ROOT, stdio: 'inherit' }
  )
  if (result.status !== 0) {
    throw new Error('go build failed')
  }
}

function wrapKey(): string {
  return randomBytes(32).toString('base64')
}

function startBackupd(binDir: string, runDir: string): ChildProcess {
  const workDir = join(runDir, 'work')
  mkdirSync(workDir)
  const log = openSync(join(runDir, 'backupd.log'), 'a')
  return spawn(join(binDir, 'backupd'), [], {
    cwd: runDir,
    stdio: ['ignore', log, log],
    env: {
      ...process.env,
      ...fixtureEnv(),
      BACKUPD_ADDR: `:${BACKUPD_PORT}`,
      STATE_BACKEND: 'memory',
      SRW_KEY: wrapKey(),
      TW_KEY: wrapKey(),
      BACKUP_WORK_DIR: workDir,
      // CI runners have no tmpfs to offer. What lands here is a test
      // Space's scratch data, and the credentials never do.
      BACKUP_WORK_DIR_ALLOW_DISK: 'true'
    }
  })
}

async function waitForReady(backupd: ChildProcess, runDir: string): Promise<void> {
  const deadline = Date.now() + READY_TIMEOUT_MS
  while (Date.now() < deadline) {
    if (backupd.exitCode !== null) {
      break
    }
    const ready = await http(`http://127.0.0.1:${BACKUPD_PORT}/readyz`, { timeoutMs: 2000 }).catch(
      () => undefined
    )
    if (ready?.status === 200) {
      return
    }
    await new Promise((resolve) => setTimeout(resolve, 500))
  }
  backupd.kill('SIGKILL')
  const tail = readFileSync(join(runDir, 'backupd.log'), 'utf8').split('\n').slice(-20).join('\n')
  throw new Error(`backupd did not become ready:\n${tail}`)
}

async function stop(backupd: ChildProcess): Promise<void> {
  if (backupd.exitCode !== null) {
    return
  }
  const exited = new Promise((resolve) => backupd.once('exit', resolve))
  backupd.kill('SIGTERM')
  const timer = setTimeout(() => backupd.kill('SIGKILL'), 10_000)
  await exited
  clearTimeout(timer)
}
