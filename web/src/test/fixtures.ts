// Wire-shaped test data, so every spec builds the same documents the server
// sends rather than its own idea of them.

import { vi } from 'vitest'
import type { BackupApi, BackupStatus, Job, Space } from '../api'

export const SPACE_ID = 'storage$space-1!space-1'

export function space(overrides: Partial<Space> = {}): Space {
  return { id: SPACE_ID, name: 'Family photos', type: 'project', role: 'editor', ...overrides }
}

export function job(overrides: Partial<Job> = {}): Job {
  return {
    id: 'job-1',
    kind: 'backup',
    state: 'succeeded',
    trigger: 'schedule',
    created_at: '2026-09-23T01:30:00Z',
    updated_at: '2026-09-23T01:40:00Z',
    finished_at: '2026-09-23T01:40:00Z',
    file_count: 1200,
    total_bytes: 3_400_000_000,
    ...overrides
  }
}

/** status is a set-up, enabled, healthy Space; override what a test is about. */
export function status(overrides: Partial<BackupStatus> = {}): BackupStatus {
  return {
    space_id: SPACE_ID,
    configured: true,
    keys_configured: true,
    stale: false,
    enabled: true,
    cron: '30 2 * * *',
    running: false,
    last_run: job(),
    last_successful_run: job(),
    next_run: '2026-09-25T00:30:00Z',
    retention_days: 90,
    ...overrides
  }
}

/** FakeApi is every client method as a mock, typed against the real client. */
export type FakeApi = { [K in keyof BackupApi]: ReturnType<typeof vi.fn> }

/** fakeApi returns a client whose every method rejects until a test says otherwise. */
export function fakeApi(): FakeApi {
  const methods: (keyof BackupApi)[] = [
    'listSpaces',
    'listTargets',
    'keyStatus',
    'setupKeys',
    'recoveryEnvelope',
    'rotateRecoveryKey',
    'backupConfig',
    'setBackupConfig',
    'patchBackupConfig',
    'schedule',
    'setSchedule',
    'status',
    'runBackup',
    'listRuns',
    'listNotifications',
    'listSnapshots',
    'restore'
  ]
  return Object.fromEntries(
    methods.map((m) => [m, vi.fn(() => Promise.reject(new Error(`unexpected call: ${m}`)))])
  ) as FakeApi
}
