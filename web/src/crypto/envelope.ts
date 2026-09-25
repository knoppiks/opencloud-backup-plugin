// Key-envelope wire format — the browser half of a LONG-TERM COMPATIBILITY
// PROMISE. See .agents/plan/key-envelope-format.md and pkg/keys/envelope.go;
// this file must stay byte-identical with them forever, because the offline
// decrypt CLI has to open what this code produces years from now.
//
// Byte layout (all integers big-endian):
//
//	offset  size  field
//	0       5     magic     "OCBKE"
//	5       1     version   format version (currently 1)
//	6       1     kind      1=RK, 2=SRW, 3=TW
//	7       1     kdf       0=none (direct 32-byte key), 1=Argon2id
//	8       4     argonTime      Argon2id time cost   (0 when kdf=none)
//	12      4     argonMemoryKiB Argon2id memory, KiB (0 when kdf=none)
//	16      1     argonLanes     Argon2id parallelism (0 when kdf=none)
//	17      1     saltLen   length of the KDF salt (0 when kdf=none)
//	18      varies salt     KDF salt
//	..      24    nonce     XChaCha20-Poly1305 nonce
//	..      rest  ciphertext AEAD(plaintext), header as additional data
//
// The whole header — through the salt, excluding the nonce — is the AEAD's
// additional data, so version, kind and KDF costs cannot be altered without
// breaking the tag.
import { xchacha20poly1305 } from '@noble/ciphers/chacha.js'
import { deriveArgon2idKey, KEK_SIZE } from './argon2'
import { concatBytes, equalBytes, randomBytes, zeroize } from './bytes'

export const ENVELOPE_MAGIC = 'OCBKE'
export const ENVELOPE_VERSION = 1

const MAGIC_BYTES = Uint8Array.from([0x4f, 0x43, 0x42, 0x4b, 0x45])
const HEADER_MIN = 18
const NONCE_SIZE = 24
const TAG_SIZE = 16

/** WrapKind says what a given envelope wraps, and under whose key. */
export const WrapKind = {
  /** RK: the Data Key under the user's Recovery Key. */
  RK: 1,
  /** SRW: the Data Key under the server's runtime key. */
  SRW: 2,
  /** TW: target credentials under the cluster's target-wrap key. */
  TW: 3
} as const
export type WrapKind = (typeof WrapKind)[keyof typeof WrapKind]

/** KdfId identifies how a secret becomes a key-encryption key. */
export const KdfId = {
  /** None: the secret is already 32 uniformly random bytes. */
  None: 0,
  /** Argon2id: the secret is human-transportable and must be stretched. */
  Argon2id: 1
} as const
export type KdfId = (typeof KdfId)[keyof typeof KdfId]

/** ArgonParams are the costs recorded in, and read back from, an envelope. */
export interface ArgonParams {
  time: number
  memoryKiB: number
  lanes: number
  saltLen: number
}

/**
 * DEFAULT_ARGON_PARAMS matches DefaultArgonParams in pkg/keys.
 *
 * Tune *above* this floor on a slow device if the ceremony feels sluggish;
 * never below MIN_ARGON_PARAMS, which the API rejects outright.
 */
export const DEFAULT_ARGON_PARAMS: ArgonParams = {
  time: 3,
  memoryKiB: 64 * 1024,
  lanes: 4,
  saltLen: 16
}

/**
 * MIN_ARGON_PARAMS is the floor POST /backup/setup enforces (pkg/keys/policy.go).
 * Every dimension is checked independently — memory cannot buy passes.
 */
export const MIN_ARGON_PARAMS: ArgonParams = {
  time: 2,
  memoryKiB: 19 * 1024,
  lanes: 1,
  saltLen: 16
}

// Ceilings from pkg/keys/envelope.go. An envelope carries its own costs, so a
// corrupted or hostile blob could otherwise ask the browser to allocate a
// gigabyte before any authentication can fail.
const MAX_ARGON_TIME = 16
const MAX_ARGON_MEMORY_KIB = 1 << 20
const MAX_ARGON_LANES = 16

/** EnvelopeErrorCode names why an envelope was refused. */
export type EnvelopeErrorCode =
  'malformed' | 'unsupported_version' | 'unknown_kdf' | 'bad_params' | 'wrong_kind' | 'weak'

/** EnvelopeError is a structural or policy failure — never a key failure. */
export class EnvelopeError extends Error {
  readonly code: EnvelopeErrorCode

  constructor(code: EnvelopeErrorCode, message: string) {
    super(message)
    this.name = 'EnvelopeError'
    this.code = code
  }
}

/**
 * UnwrapError is authentication failure: a wrong key, or a tampered blob.
 *
 * It deliberately carries no detail. Telling the two apart would tell an
 * attacker holding the ciphertext when they had guessed part of the key, and
 * tells a user nothing they can act on — in both cases the answer is "this is
 * not the Recovery Key for this backup".
 */
export class UnwrapError extends Error {
  constructor() {
    super('cannot unwrap (wrong key or tampered envelope)')
    this.name = 'UnwrapError'
  }
}

/** EnvelopeInfo is an envelope's metadata: safe to display, carries no secret. */
export interface EnvelopeInfo {
  version: number
  kind: WrapKind
  kdf: 'argon2id' | 'none'
  argon: ArgonParams
}

interface ParsedEnvelope {
  info: EnvelopeInfo
  kdf: KdfId
  salt: Uint8Array
  nonce: Uint8Array
  ciphertext: Uint8Array
  /** header is the exact AEAD additional data. */
  header: Uint8Array
}

/** buildHeader serialises the envelope header. */
function buildHeader(
  version: number,
  kind: WrapKind,
  kdf: KdfId,
  params: ArgonParams,
  salt: Uint8Array
): Uint8Array {
  const header = new Uint8Array(HEADER_MIN + salt.length)
  header.set(MAGIC_BYTES, 0)
  header[5] = version
  header[6] = kind
  header[7] = kdf

  const view = new DataView(header.buffer, header.byteOffset, header.byteLength)
  view.setUint32(8, kdf === KdfId.Argon2id ? params.time : 0, false)
  view.setUint32(12, kdf === KdfId.Argon2id ? params.memoryKiB : 0, false)
  header[16] = kdf === KdfId.Argon2id ? params.lanes : 0
  header[17] = salt.length
  header.set(salt, HEADER_MIN)
  return header
}

/** parseEnvelope validates the header and splits the blob. It decrypts nothing. */
function parseEnvelope(blob: Uint8Array): ParsedEnvelope {
  if (blob.length < HEADER_MIN) {
    throw new EnvelopeError('malformed', 'envelope is too short')
  }
  if (!equalBytes(blob.subarray(0, MAGIC_BYTES.length), MAGIC_BYTES)) {
    throw new EnvelopeError('malformed', 'envelope has the wrong magic')
  }

  const version = blob[5] as number
  if (version === 0 || version > ENVELOPE_VERSION) {
    throw new EnvelopeError('unsupported_version', `unsupported envelope version ${version}`)
  }

  const kdf = blob[7] as number
  if (kdf !== KdfId.None && kdf !== KdfId.Argon2id) {
    throw new EnvelopeError('unknown_kdf', 'envelope uses an unknown key derivation function')
  }

  const view = new DataView(blob.buffer, blob.byteOffset, blob.byteLength)
  const saltLen = blob[17] as number
  const params: ArgonParams = {
    time: view.getUint32(8, false),
    memoryKiB: view.getUint32(12, false),
    lanes: blob[16] as number,
    saltLen
  }
  if (kdf === KdfId.Argon2id) {
    assertArgonWithinLimits(params)
  }

  const headerLen = HEADER_MIN + saltLen
  if (blob.length < headerLen + NONCE_SIZE + TAG_SIZE) {
    throw new EnvelopeError('malformed', 'envelope is truncated')
  }

  return {
    info: {
      version,
      kind: blob[6] as WrapKind,
      kdf: kdf === KdfId.Argon2id ? 'argon2id' : 'none',
      argon: params
    },
    kdf,
    salt: blob.subarray(HEADER_MIN, headerLen),
    nonce: blob.subarray(headerLen, headerLen + NONCE_SIZE),
    ciphertext: blob.subarray(headerLen + NONCE_SIZE),
    header: blob.subarray(0, headerLen)
  }
}

/**
 * assertArgonWithinLimits range-checks costs *before* any derivation, which is
 * the only moment it helps: past that point the memory is already allocated.
 */
function assertArgonWithinLimits(params: ArgonParams): void {
  if (params.time === 0 || params.memoryKiB === 0 || params.lanes === 0) {
    throw new EnvelopeError('bad_params', 'invalid argon2id parameters')
  }
  if (params.time > MAX_ARGON_TIME) {
    throw new EnvelopeError('bad_params', 'argon2id time cost out of range')
  }
  if (params.memoryKiB > MAX_ARGON_MEMORY_KIB) {
    throw new EnvelopeError('bad_params', 'argon2id memory cost out of range')
  }
  if (params.lanes > MAX_ARGON_LANES) {
    throw new EnvelopeError('bad_params', 'argon2id parallelism out of range')
  }
}

/** inspect reports an envelope's metadata without needing any key. */
export function inspect(blob: Uint8Array): EnvelopeInfo {
  return parseEnvelope(blob).info
}

/**
 * checkRecoveryEnvelope mirrors the server's accept/reject decision for
 * POST /backup/setup, so the browser refuses an envelope before the ceremony
 * shows the user a Recovery Key the API will not take.
 */
export function checkRecoveryEnvelope(blob: Uint8Array): EnvelopeInfo {
  const info = inspect(blob)
  if (info.kind !== WrapKind.RK) {
    throw new EnvelopeError('wrong_kind', 'envelope is not a recovery envelope')
  }
  if (info.kdf !== 'argon2id') {
    throw new EnvelopeError('unknown_kdf', 'recovery envelope must use argon2id')
  }
  const { argon } = info
  if (argon.time < MIN_ARGON_PARAMS.time) {
    throw new EnvelopeError('weak', 'recovery envelope uses too few passes')
  }
  if (argon.memoryKiB < MIN_ARGON_PARAMS.memoryKiB) {
    throw new EnvelopeError('weak', 'recovery envelope uses too little memory')
  }
  if (argon.lanes < MIN_ARGON_PARAMS.lanes) {
    throw new EnvelopeError('weak', 'recovery envelope uses too little parallelism')
  }
  if (argon.saltLen < MIN_ARGON_PARAMS.saltLen) {
    throw new EnvelopeError('weak', 'recovery envelope salt is too short')
  }
  return info
}

/** SealOptions describe one envelope to produce. */
export interface SealOptions {
  plaintext: Uint8Array
  /** secret is the RK entropy for argon2id, or a 32-byte key for kdf=none. */
  secret: Uint8Array
  kind: WrapKind
  kdf?: KdfId
  params?: ArgonParams
  /**
   * salt and nonce exist for the interop vectors only. Production omits both
   * and takes them from the platform CSPRNG; passing them makes the output
   * reproducible, which is exactly what an envelope must never be in the field.
   */
  salt?: Uint8Array
  nonce?: Uint8Array
}

/** seal produces a self-contained envelope. */
export async function seal(options: SealOptions): Promise<Uint8Array> {
  const kdf = options.kdf ?? KdfId.Argon2id
  const params = options.params ?? DEFAULT_ARGON_PARAMS

  if (options.plaintext.length === 0) {
    throw new EnvelopeError('malformed', 'refusing to seal empty plaintext')
  }
  if (options.secret.length === 0) {
    throw new EnvelopeError('malformed', 'refusing to seal under an empty secret')
  }

  let salt: Uint8Array = new Uint8Array(0)
  if (kdf === KdfId.Argon2id) {
    assertArgonWithinLimits(params)
    salt = options.salt ?? randomBytes(params.saltLen)
    if (salt.length !== params.saltLen) {
      throw new EnvelopeError('bad_params', 'salt length does not match the recorded parameters')
    }
  } else if (options.secret.length !== KEK_SIZE) {
    throw new EnvelopeError('bad_params', `direct key must be ${KEK_SIZE} bytes`)
  }

  const header = buildHeader(ENVELOPE_VERSION, options.kind, kdf, params, salt)
  const nonce = options.nonce ?? randomBytes(NONCE_SIZE)
  if (nonce.length !== NONCE_SIZE) {
    throw new EnvelopeError('bad_params', `nonce must be ${NONCE_SIZE} bytes`)
  }

  const kek = await deriveKek(options.secret, salt, kdf, params)
  try {
    const sealed = xchacha20poly1305(kek, nonce, header).encrypt(options.plaintext)
    return concatBytes(header, nonce, sealed)
  } finally {
    zeroize(kek)
  }
}

/** open unwraps an envelope, returning its plaintext. */
export async function open(blob: Uint8Array, secret: Uint8Array): Promise<Uint8Array> {
  const parsed = parseEnvelope(blob)
  const kek = await deriveKek(secret, parsed.salt, parsed.kdf, parsed.info.argon)
  try {
    return xchacha20poly1305(kek, parsed.nonce, parsed.header).decrypt(parsed.ciphertext)
  } catch {
    // Never distinguish a wrong key from a tampered blob, and never echo any
    // part of either into the message.
    throw new UnwrapError()
  } finally {
    zeroize(kek)
  }
}

async function deriveKek(
  secret: Uint8Array,
  salt: Uint8Array,
  kdf: KdfId,
  params: ArgonParams
): Promise<Uint8Array> {
  if (kdf === KdfId.Argon2id) {
    return await deriveArgon2idKey(secret, salt, params)
  }
  if (secret.length !== KEK_SIZE) {
    throw new EnvelopeError('bad_params', `direct key must be ${KEK_SIZE} bytes`)
  }
  return Uint8Array.from(secret)
}
