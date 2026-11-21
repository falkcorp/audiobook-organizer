// file: web/src/lib/storageKeys.ts
// version: 1.6.0
// guid: 5c8a3d7b-2e1f-4a9c-b3d5-1e8f2a9c7d4b
// last-edited: 2026-09-27

import { BOOKS_STORAGE_KEYS } from '../modules/books/storageKeys';

/**
 * Centralised localStorage key constants. The audiobook module's keys live in
 * modules/books/storageKeys.ts and are spread in here, so every existing
 * STORAGE_KEYS.X reference keeps working. What is spelled out below is bound
 * for the shell (storageKey() names at F9-03) or stays as it is
 * (APP_THEME_MODE is the preset's theme key).
 */
export const STORAGE_KEYS = {
  ...BOOKS_STORAGE_KEYS,
  WELCOME_WIZARD_COMPLETED: 'welcome_wizard_completed',
  ACTIVITY_SOURCE_PREFS: 'activity-source-prefs',
  ACTIVITY_OPS_PINNED: 'activity-ops-pinned',
  TABLE_CONFIG: 'table_config',
  APP_THEME_MODE: 'app-theme-mode',
  DISMISSED_ANNOUNCEMENTS: 'dismissed_announcements',
} as const;

export type StorageKey = typeof STORAGE_KEYS[keyof typeof STORAGE_KEYS];
