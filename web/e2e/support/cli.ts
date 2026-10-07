// The offline recovery path: `takeout` copies a Space's backups out of the
// bucket, and `decrypt` opens them with a Recovery Key. Both are the real
// binaries, built by global-setup.ts.
//
// The key reaches `decrypt` on stdin, the way the tool reads it without a
// terminal. It is never a flag: command lines are visible to every process.

import { spawnSync } from 'node:child_process'
import { join } from 'node:path'
import { GARAGE, garageKeys, runContext } from './env'

export interface CliResult {
  status: number | null
  output: string
}

function run(binary: string, args: string[], env: NodeJS.ProcessEnv, input?: string): CliResult {
  const result = spawnSync(join(runContext().binDir, binary), args, {
    env: { PATH: process.env.PATH, HOME: process.env.HOME, ...env },
    input: input ?? '',
    encoding: 'utf8',
    timeout: 120_000
  })
  return { status: result.status, output: `${result.stdout}${result.stderr}` }
}

/** takeout extracts one Space's backups into `outDir`. */
export function takeout(spaceId: string, outDir: string): CliResult {
  const keys = garageKeys()
  return run(
    'takeout',
    [
      '-endpoint',
      GARAGE.endpoint,
      '-region',
      GARAGE.region,
      '-bucket',
      GARAGE.bucket,
      '-prefix',
      runContext().prefix,
      '-space',
      spaceId,
      '-plain-http',
      '-out',
      outDir
    ],
    { S3_ACCESS_KEY_ID: keys.accessKeyId, S3_SECRET_ACCESS_KEY: keys.secretAccessKey }
  )
}

/**
 * decrypt restores the newest backup in a take-out to `outDir` with `key`.
 * `envelope` is a downloaded recovery.ocbke to use instead of the take-out's.
 */
export function decrypt(
  takeoutDir: string,
  outDir: string,
  key: string,
  envelope?: string
): CliResult {
  const args = ['-in', takeoutDir, '-out', outDir]
  if (envelope) {
    args.push('-envelope', envelope)
  }
  return run('decrypt', args, {}, `${key}\n`)
}
