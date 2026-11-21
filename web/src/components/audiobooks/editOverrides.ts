// file: web/src/components/audiobooks/editOverrides.ts
// version: 1.0.0
// guid: 7c1e5b0a-3d84-4f6e-9a52-b8d0e7f41c36
// last-edited: 2026-11-20

import type { Audiobook } from '../../types';
import type { OverridePayload } from '../../services/api';

/**
 * Maps the metadata edit dialog's form field names to the API / field-state
 * keys. This is also the list of fields the dialog can lock: a field with no
 * entry here has no server-side lock, so the dialog renders no padlock for it.
 */
export const FIELD_TO_API: Record<string, string> = {
  title: 'title',
  author: 'author_name',
  narrator: 'narrator',
  series: 'series_name',
  series_number: 'series_position',
  genre: 'genre',
  year: 'audiobook_release_year',
  language: 'language',
  publisher: 'publisher',
  isbn10: 'isbn10',
  isbn13: 'isbn13',
  description: 'description',
};

/**
 * Builds the PUT /audiobooks/:id `overrides` map from a dialog save.
 *
 * - A dirty field becomes `{ value, locked }`; it is locked unless the user
 *   explicitly unlocked it (`lockChanges[field] === false`).
 * - A non-dirty field the user toggled becomes a lock-only `{ locked }`. It
 *   carries no `value`: a value would make the server record an edit.
 * - Fields without a FIELD_TO_API entry are skipped.
 */
export function buildOverrides(
  updated: Audiobook,
  dirtyFields: Set<string>,
  lockChanges: Record<string, boolean>
): Record<string, OverridePayload> {
  const overrides: Record<string, OverridePayload> = {};
  const record = updated as unknown as Record<string, unknown>;

  for (const field of dirtyFields) {
    const apiField = FIELD_TO_API[field];
    if (!apiField) continue;
    const value =
      field === 'year'
        ? (updated.year ?? updated.audiobook_release_year ?? null)
        : field === 'series_number'
          ? (updated.series_number ?? null)
          : (record[field] ?? null);
    overrides[apiField] = { value, locked: lockChanges[field] ?? true };
  }

  for (const [field, locked] of Object.entries(lockChanges)) {
    if (dirtyFields.has(field)) continue;
    const apiField = FIELD_TO_API[field];
    if (!apiField) continue;
    overrides[apiField] = { locked };
  }

  return overrides;
}
