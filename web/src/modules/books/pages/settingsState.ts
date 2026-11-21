// file: web/src/modules/books/pages/settingsState.ts
// version: 0.1.0
// guid: 697d7308-8b25-4baa-bc15-2ce0d2d9dd20
// last-edited: 2026-11-21

// The Settings page's form state, shared by every books panel, the save bar and the
// settings hooks.

import type { UiMetadataSource } from '../../../utils/metadataSources';

export type { UiMetadataSource };

export interface SettingsState {
  libraryPath: string;
  /** Location of the MAIN book database. Takes effect on restart. */
  databasePath: string;
  organizationStrategy: string;
  scanOnStartup: boolean;
  autoOrganize: boolean;
  folderNamingPattern: string;
  fileNamingPattern: string;
  createBackups: boolean;
  supportedExtensions: string[];
  excludePatterns: string[];
  enableDiskQuota: boolean;
  diskQuotaPercent: number;
  enableUserQuotas: boolean;
  enableAIParsing: boolean;
  openaiApiKey: string;
  metadataSources: UiMetadataSource[];
  concurrentScans: number;
  cacheInvalidateOnBookUpdate: boolean;
  metadataFetchCacheTTLDays: number;
  logLevel: string;
  purgeSoftDeletedAfterDays: number;
  backupRetentionDays: number;
  purgeSoftDeletedDeleteFiles: boolean;
  autoUpdateEnabled: boolean;
  autoUpdateChannel: string;
  autoUpdateCheckMinutes: number;
  maintenanceWindowEnabled: boolean;
  maintenanceWindowStart: number;
  maintenanceWindowEnd: number;
  autoRenameOnApply: boolean;
  autoWriteTagsOnApply: boolean;
  verifyAfterWrite: boolean;

  // Deluge integration
  protectedPaths: string;
}
