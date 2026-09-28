// Cross-implementation interop: this file is the reason the crypto was built
// first.
//
// Nothing in the running system compares browser-produced key material against
// Go's. The server stores whatever envelope it is handed and answers 201; a
// mismatched Argon2id parameter, a wrong-endian header field or a different
// base32 alphabet all produce a green status and a Recovery Key that opens
// nothing — discovered years later, by a user who has already lost everything
// else.
//
// pkg/keys/testdata/vectors.json is the shared fixture. Both implementations
// derive the same outputs from the same inputs; regenerate it with
// `go test ./pkg/keys -run TestGoldenVectors -update`.
import { describe, expect, it } from 'vitest'
// The fixture is imported straight out of the Go package that owns it, so a
// regenerated vectors.json reaches this suite without a copy step — and a copy
// step is exactly how two implementations drift apart while both stay green.
import vectorsJson from '../../../pkg/keys/testdata/vectors.json'
import { bytesToHex, hexToBytes } from './bytes'
import {
  checkRecoveryEnvelope,
  inspect,
  KdfId,
  open,
  seal,
  WrapKind,
  type ArgonParams
} from './envelope'
import { decodeRecoveryKey, encodeRecoveryKey, RecoveryKeyError } from './recoverykey'
import { deriveArgon2idKey } from './argon2'

interface ArgonJson {
  time: number
  memory_kib: number
  lanes: number
  salt_len: number
}

interface VectorFile {
  envelope_version: number
  magic: string
  rk_prefix: string
  min_argon_params: ArgonJson
  default_argon_params: ArgonJson
  recovery_keys: Array<{ name: string; entropy_hex: string; display: string }>
  recovery_key_decodes: Array<{
    name: string
    input: string
    entropy_hex?: string
    error?: string
  }>
  envelopes: Array<{
    name: string
    note: string
    kind: number
    kdf: 'argon2id' | 'none'
    argon?: ArgonJson
    secret_hex: string
    rk_display?: string
    salt_hex: string
    nonce_hex: string
    plaintext_hex: string
    kek_hex: string
    header_hex: string
    envelope_hex: string
    envelope_b64: string
    accepted_by_setup: boolean
  }>
}

const vectors = vectorsJson as VectorFile

function toParams(argon: ArgonJson): ArgonParams {
  return {
    time: argon.time,
    memoryKiB: argon.memory_kib,
    lanes: argon.lanes,
    saltLen: argon.salt_len
  }
}

describe('envelope vectors', () => {
  it('agrees with Go on the format constants', () => {
    expect(vectors.magic).toBe('OCBKE')
    expect(vectors.envelope_version).toBe(1)
    expect(vectors.rk_prefix).toBe('ocbk1')
  })

  for (const vector of vectors.envelopes) {
    describe(vector.name, () => {
      const secret = hexToBytes(vector.secret_hex)
      const salt = hexToBytes(vector.salt_hex)
      const nonce = hexToBytes(vector.nonce_hex)
      const plaintext = hexToBytes(vector.plaintext_hex)
      const blob = hexToBytes(vector.envelope_hex)
      const argon = vector.argon ? toParams(vector.argon) : undefined
      const kdf = vector.kdf === 'argon2id' ? KdfId.Argon2id : KdfId.None

      it('derives the same key-encryption key', async () => {
        if (!argon) {
          // kdf=none uses the 32-byte secret verbatim; there is nothing to
          // derive, and the vector records that by repeating the secret.
          expect(vector.kek_hex).toBe(vector.secret_hex)
          return
        }
        const kek = await deriveArgon2idKey(secret, salt, argon)
        expect(bytesToHex(kek)).toBe(vector.kek_hex)
      })

      it('seals to the same bytes', async () => {
        const sealed = await seal({
          plaintext,
          secret,
          kind: vector.kind as WrapKind,
          kdf,
          ...(argon ? { params: argon } : {}),
          salt,
          nonce
        })
        expect(bytesToHex(sealed)).toBe(vector.envelope_hex)
        expect(bytesToHex(sealed.subarray(0, vector.header_hex.length / 2))).toBe(vector.header_hex)
      })

      it('opens the envelope Go sealed', async () => {
        const recovered = await open(blob, secret)
        expect(bytesToHex(recovered)).toBe(vector.plaintext_hex)
      })

      it('reports the same metadata', () => {
        const info = inspect(blob)
        expect(info.version).toBe(vectors.envelope_version)
        expect(info.kind).toBe(vector.kind)
        expect(info.kdf).toBe(vector.kdf)
        if (argon) {
          expect(info.argon).toEqual(argon)
        }
      })

      it('agrees on whether setup would accept it', () => {
        let accepted = true
        try {
          checkRecoveryEnvelope(blob)
        } catch {
          accepted = false
        }
        expect(accepted).toBe(vector.accepted_by_setup)
      })

      it('refuses the wrong key without saying why', async () => {
        const wrong = Uint8Array.from(secret)
        wrong[0] = (wrong[0]! ^ 0xff) & 0xff
        await expect(open(blob, wrong)).rejects.toThrow(/cannot unwrap/)
      })

      it('refuses a tampered header', async () => {
        const tampered = Uint8Array.from(blob)
        // Flip a bit inside the salt: it is authenticated as additional data,
        // so the tag must fail even though the ciphertext is untouched.
        tampered[18] = (tampered[18]! ^ 0x01) & 0xff
        await expect(open(tampered, secret)).rejects.toThrow(/cannot unwrap/)
      })
    })
  }
})

describe('recovery key vectors', () => {
  for (const vector of vectors.recovery_keys) {
    it(`encodes ${vector.name} exactly as Go does`, () => {
      expect(encodeRecoveryKey(hexToBytes(vector.entropy_hex))).toBe(vector.display)
    })

    it(`decodes ${vector.name} back to the same entropy`, () => {
      expect(bytesToHex(decodeRecoveryKey(vector.display))).toBe(vector.entropy_hex)
    })
  }

  for (const vector of vectors.recovery_key_decodes) {
    it(`handles "${vector.name}" the same way`, () => {
      if (vector.error) {
        // The reason code matters as much as the rejection: "mistyped" and
        // "wrong key" send a user in opposite directions.
        expect(() => decodeRecoveryKey(vector.input)).toThrowError(
          expect.objectContaining({ name: 'RecoveryKeyError', reason: vector.error })
        )
        return
      }
      expect(bytesToHex(decodeRecoveryKey(vector.input))).toBe(vector.entropy_hex)
    })
  }

  it('rejects entropy of the wrong length before encoding it', () => {
    expect(() => encodeRecoveryKey(new Uint8Array(19))).toThrow(RecoveryKeyError)
  })
})
