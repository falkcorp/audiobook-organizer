// file: web/src/utils/trashCleanupResult.test.ts
// version: 1.5.0
// guid: 63a5ee65-33c3-4d7d-b8dc-cc5de376cdcd
// last-edited: 2026-10-29

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
      cannot_tell: 0,
      cannot_tell_version_ids: [],
      cannot_tell_book_ids: [],
      nothing_served: 0,
      nothing_served_version_ids: [],
      nothing_served_book_ids: [],
      nothing_served_file_paths: [],
      served_kept: 0,
      served_kept_version_ids: [],
      served_kept_book_ids: [],
      served_kept_file_paths: [],
      swap_incomplete: 0,
      swap_incomplete_version_ids: [],
      swap_incomplete_book_ids: [],
      swap_incomplete_file_paths: [],
      kept_sweep_report_only: false,
      kept_sweep_would_remove: 0,
      kept_sweep_would_drop: 0,
      kept_sweep_would_remove_files: [],
      kept_sweep_would_drop_files: [],
      kept_sweep_removed: 0,
      kept_sweep_gone: 0,
      kept_sweep_still_kept: 0,
      kept_sweep_kept_on_error: 0,
      kept_sweep_escalated: 0,
      kept_sweep_escalated_version_ids: [],
      kept_sweep_escalated_files: [],
      stopped_for_merge: false,
      stopped_for_merge_nights: 0,
      stopped_for_merge_repeatedly: false,
    });
  });

  it('reads the kept-file sweep report, each file with its version, book and reason', () => {
    const r = parseTrashCleanupResult({
      kept_sweep_report_only: true,
      kept_sweep_would_remove: 1,
      kept_sweep_would_remove_files: [
        {
          version_id: 'v-year-one',
          book_id: 'b-year-one',
          path: '/lib/Gotham/Year One/.versions/v-year-one/part1.m4b',
          reason: 'book_file x of book y still names it',
        },
        'not a file',
        { path: 7 },
      ],
      kept_sweep_would_drop: 1,
      kept_sweep_would_drop_files: [
        { version_id: 'v2', book_id: 'b2', path: '/lib/gone.m4b', reason: 'kept once' },
      ],
      kept_sweep_escalated: 1,
      kept_sweep_escalated_version_ids: ['v3'],
      kept_sweep_escalated_files: [
        { version_id: 'v3', book_id: 'b3', path: '/lib/stuck.m4b', reason: 'owner lookup down' },
      ],
    });
    expect(r?.kept_sweep_report_only).toBe(true);
    expect(r?.kept_sweep_would_remove).toBe(1);
    expect(r?.kept_sweep_would_remove_files).toEqual([
      {
        version_id: 'v-year-one',
        book_id: 'b-year-one',
        path: '/lib/Gotham/Year One/.versions/v-year-one/part1.m4b',
        reason: 'book_file x of book y still names it',
      },
      { version_id: '', book_id: '', path: '', reason: '' },
    ]);
    expect(r?.kept_sweep_would_drop_files[0].path).toBe('/lib/gone.m4b');
    expect(r?.kept_sweep_escalated_version_ids).toEqual(['v3']);
    expect(r?.kept_sweep_escalated_files[0].reason).toBe('owner lookup down');
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

  it('reads the served-file set: ids, distinct books and paths', () => {
    const r = parseTrashCleanupResult({
      purged: 0,
      served_kept: 2,
      served_kept_version_ids: ['v-a', 'v-b'],
      served_kept_book_ids: ['b-a', 'b-a'],
      served_kept_file_paths: ['/lib/Gotham/Year One/part1.m4b'],
    });
    expect(r?.served_kept).toBe(2);
    expect(cleanupVersionIds(r!, 'served_kept')).toEqual(['v-a', 'v-b']);
    expect(cleanupBookIds(r!, 'served_kept')).toEqual(['b-a']);
    expect(r?.served_kept_file_paths).toEqual(['/lib/Gotham/Year One/part1.m4b']);
  });

  it("reads the nothing-served set: ids, distinct books and the books' own paths", () => {
    const r = parseTrashCleanupResult({
      purged: 0,
      nothing_served: 2,
      nothing_served_version_ids: ['v-a', 'v-b'],
      nothing_served_book_ids: ['b-a', 'b-a'],
      nothing_served_file_paths: ['/lib/Gotham/Zero Year/zero-year.m4b'],
    });
    expect(r?.nothing_served).toBe(2);
    expect(cleanupVersionIds(r!, 'nothing_served')).toEqual(['v-a', 'v-b']);
    expect(cleanupBookIds(r!, 'nothing_served')).toEqual(['b-a']);
    expect(r?.nothing_served_file_paths).toEqual(['/lib/Gotham/Zero Year/zero-year.m4b']);
    // Not mixed up with the cannot-tell set.
    expect(cleanupVersionIds(r!, 'cannot_tell')).toEqual([]);
  });

  it('reads the unfinished-swap set apart from the others', () => {
    const r = parseTrashCleanupResult({
      purged: 0,
      swap_incomplete: 2,
      swap_incomplete_version_ids: ['v-out', 'v-in'],
      swap_incomplete_book_ids: ['b-long-halloween', 'b-long-halloween'],
      swap_incomplete_file_paths: ['/lib/Gotham/The Long Halloween'],
    });
    expect(r?.swap_incomplete).toBe(2);
    expect(cleanupVersionIds(r!, 'swap_incomplete')).toEqual(['v-out', 'v-in']);
    expect(cleanupBookIds(r!, 'swap_incomplete')).toEqual(['b-long-halloween']);
    expect(r?.swap_incomplete_file_paths).toEqual(['/lib/Gotham/The Long Halloween']);
    expect(cleanupVersionIds(r!, 'nothing_served')).toEqual([]);
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
    expect(cleanupVersionsLink('op-3', 'cannot_tell')).toBe('/versions?op=op-3&set=cannot_tell');
    expect(cleanupSetTab('cannot_tell')).toBe('trash');
    expect(parseCleanupSet('cannot_tell')).toBe('cannot_tell');
    expect(cleanupVersionsLink('op-5', 'nothing_served')).toBe(
      '/versions?op=op-5&set=nothing_served'
    );
    expect(cleanupSetTab('nothing_served')).toBe('trash');
    expect(parseCleanupSet('nothing_served')).toBe('nothing_served');
    expect(cleanupVersionsLink('op-4', 'served_kept')).toBe('/versions?op=op-4&set=served_kept');
    expect(cleanupSetTab('served_kept')).toBe('trash');
    expect(parseCleanupSet('served_kept')).toBe('served_kept');
    // The versions of an unfinished swap are in no trash: their books are on their own tab.
    expect(cleanupVersionsLink('op-6', 'swap_incomplete')).toBe(
      '/versions?op=op-6&set=swap_incomplete'
    );
    expect(cleanupSetTab('swap_incomplete')).toBe('swaps');
    expect(parseCleanupSet('swap_incomplete')).toBe('swap_incomplete');
  });

  it('reads back only the known sets', () => {
    expect(parseCleanupSet('kept')).toBe('kept');
    expect(parseCleanupSet('needs_attention')).toBe('needs_attention');
    expect(parseCleanupSet('other')).toBeNull();
    expect(parseCleanupSet(null)).toBeNull();
  });

  it('recognises only the maintenance def as the trash cleanup', () => {
    expect(isTrashCleanupDef('maintenance.trash-cleanup')).toBe(true);
    expect(isTrashCleanupDef('maintenance.compact-activity-log')).toBe(false);
    expect(isTrashCleanupDef(undefined)).toBe(false);
  });
});
