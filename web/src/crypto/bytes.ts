// Byte helpers shared by the envelope and Recovery Key implementations.
//
// Nothing here is cryptography; it is the encoding glue that has to agree with
// Go exactly. `base64Encode` in particular must produce the standard alphabet
// *with* padding, because the API decodes with Go's base64.StdEncoding, which
// refuses unpadded input.
import { concatBytes, equalBytes } from '@noble/ciphers/utils.js'

export { concatBytes, equalBytes }
export { bytesToHex, hexToBytes } from '@noble/ciphers/utils.js'

/** base64Encode renders bytes in the standard alphabet, padded. */
export function base64Encode(bytes: Uint8Array): string {
  let binary = ''
  for (const byte of bytes) {
    binary += String.fromCharCode(byte)
  }
  return btoa(binary)
}

/** base64Decode parses standard, padded base64. */
export function base64Decode(value: string): Uint8Array {
  const binary = atob(value)
  const out = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) {
    out[i] = binary.charCodeAt(i)
  }
  return out
}

/**
 * randomBytes draws from the platform CSPRNG.
 *
 * It fails loudly rather than falling back: a Recovery Key generated from a
 * predictable source is worse than no Recovery Key, because the user files it
 * away believing it protects them.
 */
export function randomBytes(length: number): Uint8Array {
  const source = globalThis.crypto
  if (!source?.getRandomValues) {
    throw new Error('backup: no cryptographically secure random source available')
  }
  return source.getRandomValues(new Uint8Array(length))
}

/**
 * zeroize overwrites a buffer in place.
 *
 * JavaScript gives no guarantee the engine has not copied the bytes elsewhere,
 * so this is the same best-effort hygiene the Go side documents, not a
 * promise. It still shortens the window in which a key sits in a live heap
 * object the debugger can print.
 */
export function zeroize(buffer: Uint8Array): void {
  buffer.fill(0)
}
