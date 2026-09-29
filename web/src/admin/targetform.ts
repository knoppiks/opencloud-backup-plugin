// The target form's rules, without the form.
//
// Mirrors what the server refuses (pkg/api/admintargets.go, `validate`) so the
// admin is told beside the field rather than by a 400 after the round trip.
// The server stays authoritative; its 400 is still shown if it comes.
//
// Credentials are write-only (decisions.md #14). Nothing here reads a key pair
// from a target: there is no such thing to read. A form holds what the admin
// typed, and `buildTargetRequest` is the only place it becomes a request body.

import type { AdminTarget, AdminTargetRequest, S3Credentials } from '../api'

/** TargetSettings is everything about a target except its keys. */
export interface TargetSettings {
  name: string
  endpoint: string
  bucket: string
  region: string
  prefix: string
  usePathStyle: boolean
  disableTls: boolean
}

/** KeyPairInput is one S3 key pair as typed. */
export interface KeyPairInput {
  accessKeyId: string
  secretAccessKey: string
}

/** CredentialsInput is both key pairs as typed. The maintenance pair is optional. */
export interface CredentialsInput {
  backup: KeyPairInput
  maintenance: KeyPairInput
}

/** TargetField names every field a problem can be reported against. */
export type TargetField =
  | 'name'
  | 'endpoint'
  | 'bucket'
  | 'backupAccessKeyId'
  | 'backupSecretAccessKey'
  | 'maintenanceAccessKeyId'
  | 'maintenanceSecretAccessKey'

/** FieldProblems maps a field to why it cannot be sent. Only 'required' exists. */
export type FieldProblems = Partial<Record<TargetField, 'required'>>

/** PairState is how much of a key pair has been typed. */
export type PairState = 'empty' | 'partial' | 'complete'

export function emptySettings(): TargetSettings {
  return {
    name: '',
    endpoint: '',
    bucket: '',
    region: '',
    prefix: '',
    usePathStyle: false,
    disableTls: false
  }
}

/** settingsFromTarget prefills the form from a stored target. Never its keys. */
export function settingsFromTarget(target: AdminTarget): TargetSettings {
  return {
    name: target.name,
    endpoint: target.endpoint,
    bucket: target.bucket,
    region: target.region ?? '',
    prefix: target.prefix ?? '',
    usePathStyle: target.use_path_style,
    disableTls: target.disable_tls
  }
}

export function emptyCredentials(): CredentialsInput {
  return {
    backup: { accessKeyId: '', secretAccessKey: '' },
    maintenance: { accessKeyId: '', secretAccessKey: '' }
  }
}

/**
 * pairState reports how much of a pair is filled in. The access key id is
 * trimmed, as the server trims it; the secret is not, as the server does not.
 */
export function pairState(pair: KeyPairInput): PairState {
  const hasId = pair.accessKeyId.trim() !== ''
  const hasSecret = pair.secretAccessKey !== ''
  if (hasId && hasSecret) {
    return 'complete'
  }
  return hasId || hasSecret ? 'partial' : 'empty'
}

/**
 * validateTarget lists what stops the form from being sent.
 *
 * `credentials` is undefined when the admin is not replacing them (editing,
 * with the section closed): the stored ones stay and nothing about keys is
 * checked. When given, the backup pair is required, and the maintenance pair
 * is either empty or complete — half of one is refused rather than dropped,
 * because it would store a target that looks separated and is not.
 */
export function validateTarget(
  settings: TargetSettings,
  credentials: CredentialsInput | undefined
): FieldProblems {
  const problems: FieldProblems = {}
  requireText(problems, 'name', settings.name)
  requireText(problems, 'endpoint', settings.endpoint)
  requireText(problems, 'bucket', settings.bucket)

  if (credentials !== undefined) {
    requirePair(problems, credentials.backup, 'backupAccessKeyId', 'backupSecretAccessKey')
    if (pairState(credentials.maintenance) === 'partial') {
      requirePair(
        problems,
        credentials.maintenance,
        'maintenanceAccessKeyId',
        'maintenanceSecretAccessKey'
      )
    }
  }
  return problems
}

/** hasProblems reports whether validateTarget found anything. */
export function hasProblems(problems: FieldProblems): boolean {
  return Object.keys(problems).length > 0
}

/**
 * buildTargetRequest turns the form into a request body.
 *
 * Without `credentials` the body carries no key pair at all, which is how the
 * server knows to keep the stored ones. With them, the maintenance pair is
 * sent only when complete; an empty one is left out, which on an update
 * removes a stored maintenance pair (the form warns about that first).
 */
export function buildTargetRequest(
  settings: TargetSettings,
  credentials: CredentialsInput | undefined
): AdminTargetRequest {
  const body: AdminTargetRequest = {
    name: settings.name,
    endpoint: settings.endpoint,
    bucket: settings.bucket,
    region: settings.region,
    prefix: settings.prefix,
    use_path_style: settings.usePathStyle,
    disable_tls: settings.disableTls
  }
  if (credentials !== undefined) {
    body.credentials = toWire(credentials.backup)
    if (pairState(credentials.maintenance) === 'complete') {
      body.maintenance_credentials = toWire(credentials.maintenance)
    }
  }
  return body
}

function toWire(pair: KeyPairInput): S3Credentials {
  return { access_key_id: pair.accessKeyId.trim(), secret_access_key: pair.secretAccessKey }
}

function requireText(problems: FieldProblems, field: TargetField, value: string): void {
  if (value.trim() === '') {
    problems[field] = 'required'
  }
}

function requirePair(
  problems: FieldProblems,
  pair: KeyPairInput,
  idField: TargetField,
  secretField: TargetField
): void {
  requireText(problems, idField, pair.accessKeyId)
  if (pair.secretAccessKey === '') {
    problems[secretField] = 'required'
  }
}
