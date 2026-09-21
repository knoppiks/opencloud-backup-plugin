// Recovery Key encoding — the browser half of a LONG-TERM COMPATIBILITY
// PROMISE. Mirrors pkg/keys/recoverykey.go exactly; the offline decrypt CLI
// must keep parsing what this produces forever.
//
// Format:
//
//	ocbk1-XXXXX-XXXXX-XXXXX-XXXXX-XXXXX-XXXX
//
//   - "ocbk1" is a versioned prefix; a future format bumps it to ocbk2.
//   - The payload is 160 bits of entropy plus an 8-bit checksum = 168 bits,
//     encoded as 34 Crockford base32 characters: six groups of five and a
//     final group of four, 46 characters with the prefix and dashes.
//   - Crockford base32 excludes I, L, O and U, so the key survives handwriting;
//     decoding is case-insensitive and maps the look-alikes I/L -> 1, O -> 0.
//   - The checksum catches a typo before the expensive Argon2id derivation, so
//     the UI can say "mistyped" instead of "wrong key". It is an integrity aid,
//     not a security control.
//
// The wrapping secret is the 20 raw entropy bytes, never the display string.
import { sha256 } from '@noble/hashes/sha2.js'
import { randomBytes } from './bytes'

/** RK_PREFIX is the versioned Recovery Key prefix. */
export const RK_PREFIX = 'ocbk1'

/** RK_ENTROPY_BYTES is the raw entropy carried by a Recovery Key (160 bits). */
export const RK_ENTROPY_BYTES = 20

/** RK_GROUP_SIZE is the number of characters per dash-separated group. */
export const RK_GROUP_SIZE = 5

const CROCKFORD = '0123456789ABCDEFGHJKMNPQRSTVWXYZ'

/**
 * RecoveryKeyErrorReason says why a key was refused.
 *
 * These codes are shared with the Go implementation through
 * pkg/keys/testdata/vectors.json. Two implementations will never phrase an
 * error identically, but they must agree on *why* a key failed: "mistyped" and
 * "wrong key" lead a user to opposite actions.
 */
export type RecoveryKeyErrorReason =
  | 'length'
  | 'version'
  | 'charset'
  | 'checksum'
  | 'padding'

/**
 * RecoveryKeyError is a malformed, mistyped, or wrong-version Recovery Key.
 *
 * It never includes the input, so a mistyped key cannot reach a log or an
 * error-reporting service.
 */
export class RecoveryKeyError extends Error {
  readonly reason: RecoveryKeyErrorReason

  constructor(reason: RecoveryKeyErrorReason, message: string) {
    super(message)
    this.name = 'RecoveryKeyError'
    this.reason = reason
  }
}

/** RecoveryKey is a freshly generated key: what to show, and what to wrap with. */
export interface RecoveryKey {
  /** display is the string to show the user exactly once. */
  display: string
  /** secret is the raw entropy used for wrapping. Zeroize it when done. */
  secret: Uint8Array
}

/**
 * generateRecoveryKey draws a fresh Recovery Key.
 *
 * The caller must show `display` once and never send it anywhere: there is no
 * server-side escrow, by design (decisions.md).
 */
export function generateRecoveryKey(): RecoveryKey {
  const secret = randomBytes(RK_ENTROPY_BYTES)
  return { display: encodeRecoveryKey(secret), secret }
}

/** encodeRecoveryKey renders raw entropy as the display form. */
export function encodeRecoveryKey(entropy: Uint8Array): string {
  if (entropy.length !== RK_ENTROPY_BYTES) {
    throw new RecoveryKeyError('length', `recovery key entropy must be ${RK_ENTROPY_BYTES} bytes`)
  }

  const payload = new Uint8Array(entropy.length + 1)
  payload.set(entropy, 0)
  payload[entropy.length] = checksumByte(entropy)

  const encoded = base32Encode(payload)
  let out = RK_PREFIX
  for (let i = 0; i < encoded.length; i += RK_GROUP_SIZE) {
    out += '-' + encoded.slice(i, i + RK_GROUP_SIZE)
  }
  return out
}

/**
 * decodeRecoveryKey parses a user-entered Recovery Key back into the raw
 * secret. It tolerates case, spaces and missing or extra dashes, and maps the
 * Crockford look-alikes, because users retype these by hand from paper.
 */
export function decodeRecoveryKey(input: string): Uint8Array {
  let cleaned = input.trim()
  const lower = cleaned.toLowerCase()

  if (lower.startsWith(RK_PREFIX + '-')) {
    cleaned = cleaned.slice(RK_PREFIX.length + 1)
  } else if (lower.startsWith(RK_PREFIX)) {
    cleaned = cleaned.slice(RK_PREFIX.length)
  } else if (lower.startsWith('ocbk') && lower.indexOf('-') > 0) {
    // A different version prefix is an explicit, actionable failure: the key is
    // fine, this client is too old to read it.
    throw new RecoveryKeyError('version', 'unsupported recovery key version')
  }

  let symbols = ''
  for (const character of cleaned.toUpperCase()) {
    switch (character) {
      case '-':
      case ' ':
      case '\t':
      case '\n':
      case '\r':
        continue
      case 'I':
      case 'L':
        symbols += '1'
        break
      case 'O':
        symbols += '0'
        break
      default:
        if (!CROCKFORD.includes(character)) {
          throw new RecoveryKeyError('charset', 'recovery key contains an illegal character')
        }
        symbols += character
    }
  }

  const payload = base32Decode(symbols)
  if (payload.length !== RK_ENTROPY_BYTES + 1) {
    throw new RecoveryKeyError('length', 'recovery key has the wrong length')
  }

  const entropy = payload.subarray(0, RK_ENTROPY_BYTES)
  if (payload[RK_ENTROPY_BYTES] !== checksumByte(entropy)) {
    throw new RecoveryKeyError('checksum', 'recovery key checksum mismatch (mistyped?)')
  }
  return Uint8Array.from(entropy)
}

/** checksumByte is a truncated SHA-256 over the entropy: a typo detector. */
function checksumByte(entropy: Uint8Array): number {
  return sha256(entropy)[0] as number
}

/** base32Encode encodes MSB-first with the Crockford alphabet, unpadded. */
function base32Encode(bytes: Uint8Array): string {
  let out = ''
  let accumulator = 0
  let bits = 0
  for (const byte of bytes) {
    accumulator = (accumulator << 8) | byte
    bits += 8
    while (bits >= 5) {
      bits -= 5
      out += CROCKFORD[(accumulator >>> bits) & 0x1f]
    }
  }
  if (bits > 0) {
    out += CROCKFORD[(accumulator << (5 - bits)) & 0x1f]
  }
  return out
}

/** base32Decode reverses base32Encode. Input is already upper-cased and filtered. */
function base32Decode(symbols: string): Uint8Array {
  const out: number[] = []
  let accumulator = 0
  let bits = 0
  for (const symbol of symbols) {
    const index = CROCKFORD.indexOf(symbol)
    if (index < 0) {
      throw new RecoveryKeyError('charset', 'recovery key contains an illegal character')
    }
    accumulator = (accumulator << 5) | index
    bits += 5
    if (bits >= 8) {
      bits -= 8
      out.push((accumulator >>> bits) & 0xff)
    }
  }
  // 21 payload bytes do not divide into 5-bit groups, so the last character
  // carries padding bits. They are always written as zero; anything else is a
  // typo confined to those bits, which would otherwise decode to the same
  // entropy and slip past the checksum.
  if (bits > 0 && (accumulator & ((1 << bits) - 1)) !== 0) {
    throw new RecoveryKeyError('padding', 'recovery key has non-zero trailing bits (mistyped?)')
  }
  return Uint8Array.from(out)
}
