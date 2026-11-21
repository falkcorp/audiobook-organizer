// file: web/src/modules/books/storageKeys.ts
// version: 0.1.0
// guid: e32e1399-b894-45be-a7ba-31d83ccb6d05
// last-edited: 2026-11-21

/**
 * The audiobook module's localStorage keys. The VALUES are what browsers
 * already hold under these names, so they never change here (a change silently
 * resets a user's saved page size, filters and review level);
 * booksStorageKeys.literal.test.ts pins every one. The shell's own keys are
 * storageKey() names under the app ID and never live in this file.
 */
export const BOOKS_STORAGE_KEYS = {
  ITUNES_IMPORT_SETTINGS: 'itunes_import_settings',
  LIBRARY_PAGE: 'library_page',
  LIBRARY_ITEMS_PER_PAGE: 'library_items_per_page',
  LIBRARY_RECENT_SEARCHES: 'library_recent_searches',
  METADATA_REVIEW_LANGUAGE_FILTER: 'metadata-review-language-filter',
  METADATA_REVIEW_PAGE_SIZE: 'metadata-review-page-size',
  /** Legacy boolean (the old Strict review switch). Read once to migrate into METADATA_REVIEW_LEVEL, then removed. */
  METADATA_REVIEW_STRICT_PRESET: 'metadata-review-strict-preset',
  /** The review-level slider: off | normal | indepth | strict. */
  METADATA_REVIEW_LEVEL: 'metadata-review-level',
  METADATA_REVIEW_BULK_APPLY_MODE: 'metadata-review-bulk-apply-mode',
  METADATA_REVIEW_SKIP_REPLACE_CONFIRM: 'metadata-review-skip-replace-confirm',
  DEDUP_PAGE_SIZE: 'dedup-page-size',
  DEDUP_MULTI_SELECT: 'dedup-multi-select',
  LIBRARY_TAG_CLOUD_EXPANDED: 'library-tag-cloud-expanded',
} as const;

export type BooksStorageKey = (typeof BOOKS_STORAGE_KEYS)[keyof typeof BOOKS_STORAGE_KEYS];
