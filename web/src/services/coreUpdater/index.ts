// file: web/src/services/coreUpdater/index.ts
// version: 0.1.0
// guid: 0f54884e-2956-4484-b207-a90e6b8aa7ca
// last-edited: 2026-11-20

// Public surface of the core updater client (F8-04b SPA pre-slice).
// UNWIRED: no running code imports this until the F8-04b flip replaces the
// /api/v1/update/* calls in services/api.ts and SettingsGeneral.tsx.

export {
  DEFAULT_TIMEOUT_MS,
  MAX_RETRY_AFTER_MS,
  UPDATER_APPLY_PATH,
  UPDATER_CHECK_PATH,
  UPDATER_PATH,
  UPDATER_ROLLBACK_PATH,
  createUpdaterClient,
  parseRetryAfterMs,
} from './client';
export type { UpdaterClient, UpdaterClientOptions } from './client';
export * from './types';
