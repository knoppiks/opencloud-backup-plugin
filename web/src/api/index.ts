// The extension's API layer: where the backend is, how to talk to it, and what
// comes back.
//
// This module may import `src/crypto`; `src/crypto` may not import this one
// (eslint.config.ts enforces the direction). The crypto layer decides what a
// request body contains, and this layer sends it.

export { ApiPathError, DEFAULT_API_PATH, resolveApiBase } from './baseurl'
export {
  ApiError,
  apiErrorFromResponse,
  isApiError,
  isKnownApiErrorCode,
  type ApiErrorCode,
  type ApiFailureCode,
  type TransportErrorCode
} from './errors'
export { BackupApi, DEFAULT_TIMEOUT_MS, type BackupApiOptions, type TokenSource } from './client'
export type {
  BackupConfig,
  BackupConfigPatch,
  BackupConfigRequest,
  BackupStatus,
  Job,
  JobKind,
  JobState,
  KeyStatus,
  Notification,
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
