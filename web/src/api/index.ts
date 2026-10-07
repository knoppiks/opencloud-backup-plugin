// The extension's API layer: where the backend is, how to talk to it, and what
// comes back.
//
// This module may import `src/crypto`; `src/crypto` may not import this one
// (eslint.config.ts enforces the direction). The crypto layer decides what a
// request body contains, and this layer sends it.
//
// AdminApi is deliberately not re-exported here: every view imports this
// barrel, and the admin client belongs in the admin chunk only. Import it from
// './admin'. Its wire types are plain types and cost nothing, so they are here.

export { ApiPathError, DEFAULT_API_PATH, resolveApiBase } from './baseurl'
export {
  ApiError,
  apiErrorFromResponse,
  asApiError,
  isApiError,
  isKnownApiErrorCode,
  mayHaveLanded,
  type ApiErrorCode,
  type ApiFailureCode,
  type TransportErrorCode
} from './errors'
export { BackupApi, DEFAULT_TIMEOUT_MS, type BackupApiOptions, type TokenSource } from './client'
export type {
  AdminTarget,
  AdminTargetRequest,
  CheckOutcome,
  CheckResult,
  CheckRole,
  Grant,
  GrantScope,
  S3Credentials,
  BackupConfig,
  BackupConfigPatch,
  BackupConfigRequest,
  BackupStatus,
  Job,
  JobKind,
  JobState,
  KeyStatus,
  Notification,
  OpenCloudVersion,
  PresetKind,
  RecoveryEnvelope,
  RestoreAccepted,
  RunAccepted,
  Schedule,
  SchedulePreset,
  ScheduleRequest,
  Snapshot,
  Space,
  SpaceRole,
  Target
} from './types'
