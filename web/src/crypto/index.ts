// Client-side key handling for the Backup Vault.
//
// This is the only part of the extension that touches key material. It has no
// Vue, no HTTP and no OpenCloud dependency, so it can be reasoned about — and
// tested against the Go implementation's vectors — on its own.
export {
  base64Decode,
  base64Encode,
  bytesToHex,
  equalBytes,
  hexToBytes,
  randomBytes,
  zeroize
} from './bytes'

export {
  checkRecoveryEnvelope,
  DEFAULT_ARGON_PARAMS,
  ENVELOPE_MAGIC,
  ENVELOPE_VERSION,
  EnvelopeError,
  inspect,
  KdfId,
  MIN_ARGON_PARAMS,
  open,
  seal,
  UnwrapError,
  WrapKind,
  type ArgonParams,
  type EnvelopeErrorCode,
  type EnvelopeInfo,
  type SealOptions
} from './envelope'

export {
  decodeRecoveryKey,
  encodeRecoveryKey,
  generateRecoveryKey,
  normalizeRecoveryKeyInput,
  RecoveryKeyError,
  recoveryKeyGroups,
  RK_ENTROPY_BYTES,
  RK_GROUP_COUNT,
  RK_GROUP_SIZE,
  RK_PREFIX,
  type RecoveryKey,
  type RecoveryKeyErrorReason
} from './recoverykey'

export {
  CeremonyError,
  DK_SIZE,
  envelopeDigest,
  performRecoveryKeyRotation,
  performSetupCeremony,
  recoverDataKey,
  recoveryKeyOpens,
  type CeremonyErrorCode,
  type RotationCeremony,
  type RotationOptions,
  type RotateRecoveryKeyRequest,
  type SetupCeremony,
  type SetupRequest
} from './ceremony'
