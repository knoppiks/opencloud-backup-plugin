// The key ceremony: everything that happens in the browser before the server
// is told anything.
//
// Two invariants live here, and only here.
//
// 1. The plaintext Recovery Key never leaves this process. It is generated
//    locally, shown once, and used locally. Nothing in this file puts it in a
//    request body (decisions.md; asserted at the network layer in the E2E
//    suite).
//
// 2. Before POSTing, the client unwraps the envelope it just built with the
//    very Recovery Key it is about to show the user, and compares the recovered
//    Data Key against the intended one. The server holds no Recovery Key, so it
//    cannot check this: it answers 201, the status turns green, backups run —
//    and the key the user filed away opens nothing. See
//    key-envelope-format.md §5.
import { base64Encode, equalBytes, randomBytes, zeroize } from './bytes'
import {
  checkRecoveryEnvelope,
  DEFAULT_ARGON_PARAMS,
  open,
  seal,
  WrapKind,
  type ArgonParams
} from './envelope'
import { decodeRecoveryKey, generateRecoveryKey } from './recoverykey'

/** DK_SIZE is the Data Key length in bytes; it is also the kopia repo password. */
export const DK_SIZE = 32

/** CeremonyErrorCode names the step that refused to continue. */
export type CeremonyErrorCode = 'self_verify_failed' | 'bad_data_key'

/**
 * CeremonyError aborts a ceremony before anything is sent.
 *
 * Every code here means the same thing operationally: do not show this
 * Recovery Key to the user, and do not POST this envelope.
 */
export class CeremonyError extends Error {
  readonly code: CeremonyErrorCode

  constructor(code: CeremonyErrorCode, message: string, options?: ErrorOptions) {
    super(message, options)
    this.name = 'CeremonyError'
    this.code = code
  }
}

/** SetupRequest is the body of POST /api/v1/spaces/{id}/backup/setup. */
export interface SetupRequest {
  /** wrapped_dk_rk is the RK-wrapped Data Key envelope, standard base64. */
  wrapped_dk_rk: string
  /**
   * data_key is the raw Data Key, standard base64.
   *
   * This is the one moment the Data Key crosses the network, so the server can
   * add its own wrap and run unattended backups (decisions.md #1). It requires
   * TLS on the same origin as OpenCloud; the deployment docs make that a
   * precondition.
   */
  data_key: string
}

/** RotateRecoveryKeyRequest is the body of POST .../backup/recovery-key/rotate. */
export interface RotateRecoveryKeyRequest {
  /** wrapped_dk_rk is the new envelope around the *same* Data Key. */
  wrapped_dk_rk: string
}

/** SetupCeremony is the result of a completed, self-verified setup ceremony. */
export interface SetupCeremony {
  /** recoveryKey is the display string: show once, never transmit, never store. */
  recoveryKey: string
  /** dataKey is the raw Data Key. Zeroize once the request has been sent. */
  dataKey: Uint8Array
  /** envelope is the RK-wrapped Data Key, raw bytes. */
  envelope: Uint8Array
  /** request is the ready-to-POST body. */
  request: SetupRequest
}

/** RotationCeremony is the result of a completed Recovery Key replacement. */
export interface RotationCeremony {
  /** recoveryKey is the new display string. The old one stops working. */
  recoveryKey: string
  /** envelope is the same Data Key under the new Recovery Key. */
  envelope: Uint8Array
  /** request is the ready-to-POST body. It carries no Data Key. */
  request: RotateRecoveryKeyRequest
}

/**
 * performSetupCeremony generates a Data Key and a Recovery Key, wraps one in
 * the other, and proves the result opens before returning it.
 *
 * `params` may be raised above the default for a fast device, but never below
 * MIN_ARGON_PARAMS: checkRecoveryEnvelope refuses those here rather than
 * letting the API return a 400 after the user has already been shown a key.
 */
export async function performSetupCeremony(
  params: ArgonParams = DEFAULT_ARGON_PARAMS
): Promise<SetupCeremony> {
  const dataKey = randomBytes(DK_SIZE)
  const recoveryKey = generateRecoveryKey()

  try {
    const envelope = await seal({
      plaintext: dataKey,
      secret: recoveryKey.secret,
      kind: WrapKind.RK,
      params
    })
    checkRecoveryEnvelope(envelope)
    await assertOpensWith(envelope, recoveryKey.display, dataKey)

    return {
      recoveryKey: recoveryKey.display,
      dataKey,
      envelope,
      request: {
        wrapped_dk_rk: base64Encode(envelope),
        data_key: base64Encode(dataKey)
      }
    }
  } finally {
    zeroize(recoveryKey.secret)
  }
}

/** RotationOptions describe a Recovery Key replacement. */
export interface RotationOptions {
  /** currentEnvelope comes from GET .../backup/recovery-envelope. */
  currentEnvelope: Uint8Array
  /** currentRecoveryKey is what the user typed. A wrong key fails here. */
  currentRecoveryKey: string
  params?: ArgonParams
}

/**
 * performRecoveryKeyRotation re-wraps the existing Data Key under a fresh
 * Recovery Key.
 *
 * The Data Key is recovered locally and never sent: the server already has its
 * own wrap of it, so the rotation endpoint takes the new envelope alone. Every
 * existing backup stays readable and nothing is re-uploaded — and the old
 * Recovery Key stops working, which the UI must say plainly.
 */
export async function performRecoveryKeyRotation(
  options: RotationOptions
): Promise<RotationCeremony> {
  const params = options.params ?? DEFAULT_ARGON_PARAMS

  // Unwrapping first means a mistyped current key fails before a new key has
  // been generated, let alone shown.
  const dataKey = await recoverDataKey(options.currentEnvelope, options.currentRecoveryKey)
  const recoveryKey = generateRecoveryKey()

  try {
    const envelope = await seal({
      plaintext: dataKey,
      secret: recoveryKey.secret,
      kind: WrapKind.RK,
      params
    })
    checkRecoveryEnvelope(envelope)
    await assertOpensWith(envelope, recoveryKey.display, dataKey)

    return {
      recoveryKey: recoveryKey.display,
      envelope,
      request: { wrapped_dk_rk: base64Encode(envelope) }
    }
  } finally {
    zeroize(recoveryKey.secret)
    zeroize(dataKey)
  }
}

/**
 * recoverDataKey unwraps an envelope with a user-supplied Recovery Key.
 *
 * It is the check behind "is this the right key?" in the UI, and the first step
 * of a rotation. It throws RecoveryKeyError for a key that cannot be a key at
 * all, and UnwrapError for one that is simply not this Space's.
 */
export async function recoverDataKey(
  envelope: Uint8Array,
  recoveryKey: string
): Promise<Uint8Array> {
  const secret = decodeRecoveryKey(recoveryKey)
  try {
    const dataKey = await open(envelope, secret)
    if (dataKey.length !== DK_SIZE) {
      throw new CeremonyError('bad_data_key', `data key must be ${DK_SIZE} bytes`)
    }
    return dataKey
  } finally {
    zeroize(secret)
  }
}

/**
 * assertOpensWith is the binding self-verification.
 *
 * It deliberately goes through the *display string*, not the entropy the
 * generator returned: the user will only ever have the string, so an encoding
 * bug that makes it decode to something else has to fail here, where it costs
 * nothing, rather than at recovery time, where it costs everything.
 */
async function assertOpensWith(
  envelope: Uint8Array,
  recoveryKey: string,
  expectedDataKey: Uint8Array
): Promise<void> {
  let recovered: Uint8Array
  try {
    recovered = await recoverDataKey(envelope, recoveryKey)
  } catch (cause) {
    throw new CeremonyError(
      'self_verify_failed',
      'the recovery key does not open the envelope it was generated for',
      { cause }
    )
  }

  try {
    if (!equalBytes(recovered, expectedDataKey)) {
      throw new CeremonyError(
        'self_verify_failed',
        'the envelope does not unwrap to the intended data key'
      )
    }
  } finally {
    zeroize(recovered)
  }
}
