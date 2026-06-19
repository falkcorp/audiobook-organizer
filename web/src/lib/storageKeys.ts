// file: web/src/lib/storageKeys.ts
// version: 1.1.0
// guid: 5c8a3d7b-2e1f-4a9c-b3d5-1e8f2a9c7d4b
// last-edited: 2026-06-19

/** Centralised localStorage key constants. */
export const STORAGE_KEYS = {
  WELCOME_WIZARD_COMPLETED: 'welcome_wizard_completed',
  ITUNES_IMPORT_SETTINGS: 'itunes_import_settings',
  LIBRARY_PAGE: 'library_page',
  LIBRARY_ITEMS_PER_PAGE: 'library_items_per_page',
  ACTIVITY_SOURCE_PREFS: 'activity-source-prefs',
  ACTIVITY_OPS_PINNED: 'activity-ops-pinned',
  TABLE_CONFIG: 'table_config',
  APP_THEME_MODE: 'app-theme-mode',
  DISMISSED_ANNOUNCEMENTS: 'dismissed_announcements',
  LIBRARY_RECENT_SEARCHES: 'library_recent_searches',
  METADATA_REVIEW_LANGUAGE_FILTER: 'metadata-review-language-filter',
  METADATA_REVIEW_PAGE_SIZE: 'metadata-review-page-size',
  DEDUP_PAGE_SIZE: 'dedup-page-size',
  DEDUP_MULTI_SELECT: 'dedup-multi-select',
} as const;

export type StorageKey = typeof STORAGE_KEYS[keyof typeof STORAGE_KEYS];
