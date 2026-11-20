// file: web/src/services/coreBackups/index.ts
// version: 0.1.0
// guid: caff3d88-0859-4ebd-9473-a54e919b1b11
// last-edited: 2026-11-20

// Public surface of the core backups client (F8-04 slice A6).
// UNWIRED: no running code imports this until the F8-04a flip replaces the
// /api/v1/backup/* calls in services/api.ts and hooks/useBackupHandlers.ts.

export {
  BACKUPS_PATH,
  BACKUPS_IMPORT_PATH,
  DEFAULT_TIMEOUT_MS,
  MAX_RETRY_AFTER_MS,
  RESTORE_PENDING_PATH,
  RESTORE_REVERT_PATH,
  backupDownloadUrl,
  createBackupsClient,
  parseRetryAfterMs,
} from './client';
export type { BackupsClient, BackupsClientOptions } from './client';
export * from './types';
