// file: web/src/modules/books/booksStorageKeys.literal.test.ts
// version: 0.1.0
// guid: c9dd12e0-bd73-4cc7-a8e9-4f26c9a48272
// last-edited: 2026-11-21

// F9-05: the books keys keep the literal values they had before the split. A
// browser holds user state under these names (saved page size, filters, review
// level); renaming one resets it with no error anywhere. The expected table is
// written out here, not derived from the module under test.

import { describe, expect, it } from 'vitest';
import { STORAGE_KEYS } from '../../lib/storageKeys';
import { BOOKS_STORAGE_KEYS } from './storageKeys';

const BASE_LITERALS: Record<string, string> = {
  ITUNES_IMPORT_SETTINGS: 'itunes_import_settings',
  LIBRARY_PAGE: 'library_page',
  LIBRARY_ITEMS_PER_PAGE: 'library_items_per_page',
  LIBRARY_RECENT_SEARCHES: 'library_recent_searches',
  METADATA_REVIEW_LANGUAGE_FILTER: 'metadata-review-language-filter',
  METADATA_REVIEW_PAGE_SIZE: 'metadata-review-page-size',
  METADATA_REVIEW_STRICT_PRESET: 'metadata-review-strict-preset',
  METADATA_REVIEW_LEVEL: 'metadata-review-level',
  METADATA_REVIEW_BULK_APPLY_MODE: 'metadata-review-bulk-apply-mode',
  METADATA_REVIEW_SKIP_REPLACE_CONFIRM: 'metadata-review-skip-replace-confirm',
  DEDUP_PAGE_SIZE: 'dedup-page-size',
  DEDUP_MULTI_SELECT: 'dedup-multi-select',
  LIBRARY_TAG_CLOUD_EXPANDED: 'library-tag-cloud-expanded',
};

// The shell-bound names stay in lib/storageKeys.ts until F9-03 moves them to storageKey().
const SHELL_BOUND_LITERALS: Record<string, string> = {
  WELCOME_WIZARD_COMPLETED: 'welcome_wizard_completed',
  ACTIVITY_SOURCE_PREFS: 'activity-source-prefs',
  ACTIVITY_OPS_PINNED: 'activity-ops-pinned',
  TABLE_CONFIG: 'table_config',
  APP_THEME_MODE: 'app-theme-mode',
  DISMISSED_ANNOUNCEMENTS: 'dismissed_announcements',
};

describe('books storage keys', () => {
  it('keeps every books key at its literal value, and adds none', () => {
    expect({ ...BOOKS_STORAGE_KEYS }).toEqual(BASE_LITERALS);
  });

  it('is what STORAGE_KEYS still serves under the same names', () => {
    for (const [name, literal] of Object.entries(BASE_LITERALS)) {
      expect((STORAGE_KEYS as Record<string, string>)[name], name).toBe(literal);
    }
  });

  it('leaves the shell-bound keys in STORAGE_KEYS at their literals, none in the books file', () => {
    for (const [name, literal] of Object.entries(SHELL_BOUND_LITERALS)) {
      expect((STORAGE_KEYS as Record<string, string>)[name], name).toBe(literal);
      expect(name in BOOKS_STORAGE_KEYS, name).toBe(false);
    }
    expect({ ...STORAGE_KEYS }).toEqual({ ...BASE_LITERALS, ...SHELL_BOUND_LITERALS });
  });

  it('has no two names on one literal', () => {
    const literals = Object.values(STORAGE_KEYS);
    expect(new Set(literals).size).toBe(literals.length);
  });
});
