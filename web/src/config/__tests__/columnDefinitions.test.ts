// file: web/src/config/__tests__/columnDefinitions.test.ts
// version: 1.1.0
// guid: b8c9d0e1-f2a3-4b5c-6d7e-8f9a0b1c2d3e
// last-edited: 2026-09-12

import { describe, it, expect } from 'vitest';
import {
  ALL_COLUMNS,
  COLUMN_MAP,
  COLUMN_CATEGORIES,
  getDefaultVisibleColumns,
  getColumnsByCategory,
  getColumnById,
  formatDuration,
  formatFileSize,
  formatDate,
  formatBoolean,
  formatNumber,
  isSortAvailable,
  scopeColumnSorts,
} from '../columnDefinitions';
import type { Audiobook } from '../../types';

const sampleBook: Audiobook = {
  id: 'test-id-123',
  title: 'The Great Adventure',
  author: 'Jane Author',
  narrator: 'John Narrator',
  series: 'Adventure Series',
  series_number: 3,
  genre: 'Fantasy',
  year: 2023,
  language: 'en',
  publisher: 'Big Publisher',
  edition: '1st',
  description: 'A great book about adventures.',
  duration_seconds: 45000,
  file_path: '/books/great-adventure.m4b',
  file_size_bytes: 524_288_000,
  format: 'm4b',
  bitrate_kbps: 128,
  codec: 'aac',
  sample_rate_hz: 44100,
  channels: 2,
  bit_depth: 16,
  quality: '128kbps AAC',
  library_state: 'organized',
  marked_for_deletion: false,
  metadata_review_status: 'matched',
  created_at: '2024-01-15T10:30:00Z',
  updated_at: '2024-06-20T14:00:00Z',
  work_id: 'work-456',
  isbn13: '978-0-123456-78-9',
  tags: ['fiction', 'adventure'],
};

describe('columnDefinitions', () => {
  describe('ALL_COLUMNS', () => {
    it('should have unique IDs', () => {
      const ids = ALL_COLUMNS.map((c) => c.id);
      expect(new Set(ids).size).toBe(ids.length);
    });

    it('should have unique sortKeys among sortable columns', () => {
      const sortable = ALL_COLUMNS.filter((c) => c.sortable);
      const keys = sortable.map((c) => c.sortKey);
      expect(new Set(keys).size).toBe(keys.length);
    });

    it('should only use valid categories', () => {
      for (const col of ALL_COLUMNS) {
        expect(COLUMN_CATEGORIES).toContain(col.category);
      }
    });

    it('should have minWidth <= defaultWidth', () => {
      for (const col of ALL_COLUMNS) {
        expect(col.minWidth).toBeLessThanOrEqual(col.defaultWidth);
      }
    });

    it('should have non-empty labels and IDs', () => {
      for (const col of ALL_COLUMNS) {
        expect(col.id.length).toBeGreaterThan(0);
        expect(col.label.length).toBeGreaterThan(0);
      }
    });

    it('should cover key Audiobook fields', () => {
      const ids = ALL_COLUMNS.map((c) => c.id);
      const expectedFields = [
        'title',
        'author',
        'narrator',
        'series',
        'genre',
        'duration_seconds',
        'file_size_bytes',
        'format',
        'library_state',
        'isbn13',
        'work_id',
        'created_at',
      ];
      for (const field of expectedFields) {
        expect(ids).toContain(field);
      }
    });
  });

  describe('COLUMN_MAP', () => {
    it('should have the same size as ALL_COLUMNS', () => {
      expect(COLUMN_MAP.size).toBe(ALL_COLUMNS.length);
    });

    it('should look up columns by ID', () => {
      const title = COLUMN_MAP.get('title');
      expect(title).toBeDefined();
      expect(title!.label).toBe('Title');
    });
  });

  describe('getDefaultVisibleColumns', () => {
    it('should return the expected default columns', () => {
      const visible = getDefaultVisibleColumns();
      const ids = visible.map((c) => c.id);
      expect(ids).toContain('title');
      expect(ids).toContain('author');
      expect(ids).toContain('narrator');
      expect(ids).toContain('series');
      expect(ids).toContain('genre');
      expect(ids).toContain('duration_seconds');
      expect(ids).toContain('file_size_bytes');
    });

    it('should not include non-default columns', () => {
      const visible = getDefaultVisibleColumns();
      const ids = visible.map((c) => c.id);
      expect(ids).not.toContain('isbn13');
      expect(ids).not.toContain('work_id');
      expect(ids).not.toContain('bitrate_kbps');
    });
  });

  describe('getColumnsByCategory', () => {
    it('should group columns into categories', () => {
      const grouped = getColumnsByCategory();
      expect(Object.keys(grouped).sort()).toEqual([...COLUMN_CATEGORIES].sort());
    });

    it('should include all columns across all categories', () => {
      const grouped = getColumnsByCategory();
      const total = Object.values(grouped).reduce((sum, cols) => sum + cols.length, 0);
      expect(total).toBe(ALL_COLUMNS.length);
    });
  });

  describe('getColumnById', () => {
    it('should return a column definition for a valid ID', () => {
      const col = getColumnById('author');
      expect(col).toBeDefined();
      expect(col!.label).toBe('Author');
      expect(col!.category).toBe('Basic');
    });

    it('should return undefined for unknown ID', () => {
      expect(getColumnById('nonexistent')).toBeUndefined();
    });
  });

  describe('accessors', () => {
    it('should extract values from an Audiobook', () => {
      const titleCol = getColumnById('title')!;
      expect(titleCol.accessor(sampleBook)).toBe('The Great Adventure');

      const durationCol = getColumnById('duration_seconds')!;
      expect(durationCol.accessor(sampleBook)).toBe(45000);

      const deletionCol = getColumnById('marked_for_deletion')!;
      expect(deletionCol.accessor(sampleBook)).toBe(false);
    });

    it('should handle tags accessor', () => {
      const tagsCol = getColumnById('tags')!;
      expect(tagsCol.accessor(sampleBook)).toBe('fiction, adventure');

      const bookNoTags: Audiobook = { ...sampleBook, tags: undefined };
      expect(tagsCol.accessor(bookNoTags)).toBe('');
    });
  });

  describe('formatters', () => {
    describe('formatDuration', () => {
      it('should format seconds to hours and minutes', () => {
        expect(formatDuration(3661)).toBe('1h 1m');
        expect(formatDuration(7200)).toBe('2h 0m');
        expect(formatDuration(300)).toBe('5m');
        expect(formatDuration(0)).toBe('0m');
      });

      it('should handle null/undefined/NaN', () => {
        expect(formatDuration(null)).toBe('');
        expect(formatDuration(undefined)).toBe('');
        expect(formatDuration(NaN)).toBe('');
      });
    });

    describe('formatFileSize', () => {
      it('should format bytes to human-readable sizes', () => {
        expect(formatFileSize(1_073_741_824)).toBe('1.00 GB');
        expect(formatFileSize(524_288_000)).toBe('500.00 MB');
        expect(formatFileSize(1_048_576)).toBe('1.00 MB');
        expect(formatFileSize(2048)).toBe('2.0 KB');
        expect(formatFileSize(500)).toBe('500 B');
      });

      it('should handle null/undefined', () => {
        expect(formatFileSize(null)).toBe('');
        expect(formatFileSize(undefined)).toBe('');
      });
    });

    describe('formatDate', () => {
      it('should format ISO date strings', () => {
        const result = formatDate('2024-01-15T10:30:00Z');
        expect(result).toBeTruthy();
        expect(result.length).toBeGreaterThan(0);
      });

      it('should handle null/undefined/empty', () => {
        expect(formatDate(null)).toBe('');
        expect(formatDate(undefined)).toBe('');
        expect(formatDate('')).toBe('');
      });
    });

    describe('formatBoolean', () => {
      it('should format booleans', () => {
        expect(formatBoolean(true)).toBe('Yes');
        expect(formatBoolean(false)).toBe('No');
      });

      it('should handle null/undefined', () => {
        expect(formatBoolean(null)).toBe('');
        expect(formatBoolean(undefined)).toBe('');
      });
    });

    describe('formatNumber', () => {
      it('should format numbers as strings', () => {
        expect(formatNumber(42)).toBe('42');
        expect(formatNumber(3.14)).toBe('3.14');
      });

      it('should handle null/undefined', () => {
        expect(formatNumber(null)).toBe('');
        expect(formatNumber(undefined)).toBe('');
      });
    });
  });
});

describe('series_position sort scoping', () => {
  const seriesNumber = (cols: typeof ALL_COLUMNS) => cols.find((c) => c.id === 'series_number');

  it('makes the Series # column unsortable without a series filter', () => {
    const scoped = scopeColumnSorts(ALL_COLUMNS, false);
    expect(seriesNumber(scoped)?.sortable).toBe(false);
    expect(scoped.find((c) => c.id === 'title')?.sortable).toBe(true);
    // ALL_COLUMNS is shared module state and must not be mutated.
    expect(seriesNumber(ALL_COLUMNS)?.sortable).toBe(true);
  });

  it('keeps the Series # column sortable with a series filter', () => {
    expect(seriesNumber(scopeColumnSorts(ALL_COLUMNS, true))?.sortable).toBe(true);
  });

  it('gates only series_position', () => {
    expect(isSortAvailable('series_position', false)).toBe(false);
    expect(isSortAvailable('series_position', true)).toBe(true);
    expect(isSortAvailable('title', false)).toBe(true);
  });
});
