// file: web/src/utils/trashCleanupResult.test.ts
// version: 1.0.1
// guid: 63a5ee65-33c3-4d7d-b8dc-cc5de376cdcd
// last-edited: 2026-10-27

import { describe, expect, it } from 'vitest';
import {
  cleanupBookIds,
  cleanupSetTab,
  cleanupVersionIds,
  cleanupVersionsLink,
  isTrashCleanupDef,
  parseCleanupSet,
  parseTrashCleanupResult,
} from './trashCleanupResult';

describe('parseTrashCleanupResult', () => {
  it('defaults every field the server omits', () => {
    const r = parseTrashCleanupResult({ purged: 3, failed: 0, kept_files: 0, needs_attention: 0 });
    expect(r).toEqual({
      purged: 3,
      failed: 0,
      kept_files: 0,
      kept_version_ids: [],
      kept_book_ids: [],
      kept_file_paths: [],
      needs_attention: 0,
      needs_attention_version_ids: [],
      needs_attention_book_ids: [],
      stopped_for_merge: false,
      stopped_for_merge_nights: 0,
      stopped_for_merge_repeatedly: false,
    });
  });

  it('reads the kept, needs-attention and stopped fields', () => {
    const r = parseTrashCleanupResult({
      purged: 4,
      failed: 1,
      kept_files: 2,
      kept_version_ids: ['v1', 'v2'],
      kept_book_ids: ['b1', 'b1'],
      kept_file_paths: ['/lib/Gotham/Year One/part1.m4b'],
      needs_attention: 1,
      needs_attention_version_ids: ['v9'],
      needs_attention_book_ids: ['b9'],
      stopped_for_merge: true,
      stopped_for_merge_nights: 3,
      stopped_for_merge_repeatedly: true,
    });
    expect(r?.stopped_for_merge_nights).toBe(3);
    expect(r?.stopped_for_merge_repeatedly).toBe(true);
    expect(r?.kept_file_paths).toEqual(['/lib/Gotham/Year One/part1.m4b']);
    expect(r?.stopped_for_merge).toBe(true);
    expect(cleanupVersionIds(r!, 'kept')).toEqual(['v1', 'v2']);
    expect(cleanupVersionIds(r!, 'needs_attention')).toEqual(['v9']);
    // Distinct books: two kept versions of one book are one book.
    expect(cleanupBookIds(r!, 'kept')).toEqual(['b1']);
    expect(cleanupBookIds(r!, 'needs_attention')).toEqual(['b9']);
  });

  it('drops non-string ids and non-numeric counts', () => {
    const r = parseTrashCleanupResult({ purged: 'x', kept_version_ids: ['v1', 7, null] });
    expect(r?.purged).toBe(0);
    expect(r?.kept_version_ids).toEqual(['v1']);
  });

  it.each([null, undefined, 'text', 4, ['a']])('is null for %j', (raw) => {
    expect(parseTrashCleanupResult(raw)).toBeNull();
  });
});

describe('links', () => {
  it('names the run and the group, and opens the tab the versions are on', () => {
    expect(cleanupVersionsLink('op 1', 'kept')).toBe('/versions?op=op%201&set=kept');
    expect(cleanupVersionsLink('op-2', 'needs_attention')).toBe(
      '/versions?op=op-2&set=needs_attention'
    );
    expect(cleanupSetTab('kept')).toBe('purged');
    expect(cleanupSetTab('needs_attention')).toBe('trash');
  });

  it('reads back only the two known sets', () => {
    expect(parseCleanupSet('kept')).toBe('kept');
    expect(parseCleanupSet('needs_attention')).toBe('needs_attention');
    expect(parseCleanupSet('other')).toBeNull();
    expect(parseCleanupSet(null)).toBeNull();
  });

  it('recognises the cleanup under its current and former id', () => {
    expect(isTrashCleanupDef('maintenance.trash-cleanup')).toBe(true);
    expect(isTrashCleanupDef('scheduler.trash-cleanup')).toBe(true);
    expect(isTrashCleanupDef('maintenance.compact-activity-log')).toBe(false);
    expect(isTrashCleanupDef(undefined)).toBe(false);
  });
});
