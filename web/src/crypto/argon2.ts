// Argon2id, matching golang.org/x/crypto/argon2 parameter for parameter.
//
// The Go side calls argon2.IDKey(secret, salt, time, memoryKiB, lanes, 32).
// The mapping to hash-wasm is:
//
//	time      -> iterations
//	memoryKiB -> memorySize   (hash-wasm also counts in KiB)
//	lanes     -> parallelism
//	32        -> hashLength
//
// Argon2 version 0x13 and the "id" variant are what both sides use; hash-wasm
// has no version switch and implements 0x13 only, which is the one thing here
// that cannot be asserted from this file. pkg/keys/testdata/vectors.json is
// where that assumption is actually checked.
//
// `parallelism` is a property of the hash, not a threading hint: a
// single-threaded WebAssembly build with lanes=4 produces the same bytes as Go
// running four goroutines. Raising it costs nothing here and matches Go.
import { argon2id } from 'hash-wasm'
import type { ArgonParams } from './envelope'

/** KEK_SIZE is the key length every envelope derives, in bytes. */
export const KEK_SIZE = 32

/**
 * deriveArgon2idKey stretches a Recovery Key secret into a key-encryption key.
 *
 * `secret` is the RK's 20 raw entropy bytes — never the display string.
 */
export async function deriveArgon2idKey(
  secret: Uint8Array,
  salt: Uint8Array,
  params: ArgonParams
): Promise<Uint8Array> {
  return await argon2id({
    password: secret,
    salt,
    iterations: params.time,
    memorySize: params.memoryKiB,
    parallelism: params.lanes,
    hashLength: KEK_SIZE,
    outputType: 'binary'
  })
}
