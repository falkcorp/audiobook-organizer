// file: web/src/pages/BookDetail.series-number.test.ts
// version: 1.0.0
// guid: 9ab55cf3-844b-4d92-88c8-527a02e7bdff
// last-edited: 2026-10-03

import { describe, it, expect } from 'vitest';
import { seriesNumberOf } from './BookDetail';
import type { Book } from '../services/api';

const base = { id: 'b1', title: 'T', file_path: '/x.m4b' } as Book;

// GET returns the position as series_sequence / series_position_raw; the
// edit dialog's Series Number box read only series_position and opened empty.
describe('seriesNumberOf', () => {
  it('prefers the raw position, keeping a decimal', () => {
    expect(seriesNumberOf({ ...base, series_sequence: 2, series_position_raw: '2.5' })).toBe(2.5);
  });
  it('falls back to series_sequence when the raw position is not a number', () => {
    expect(seriesNumberOf({ ...base, series_sequence: 3, series_position_raw: 'Book 3' })).toBe(3);
  });
  it('reads series_sequence when there is no raw position', () => {
    expect(seriesNumberOf({ ...base, series_sequence: 8 })).toBe(8);
  });
  it('is undefined for a book with no position', () => {
    expect(seriesNumberOf(base)).toBeUndefined();
  });
});
