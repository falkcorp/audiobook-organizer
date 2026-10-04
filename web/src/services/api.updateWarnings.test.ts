// file: web/src/services/api.updateWarnings.test.ts
// version: 1.0.0
// guid: 96e196d0-c392-43a2-9c17-4bd43bbbde52
// last-edited: 2026-10-04

import { describe, expect, it } from 'vitest';
import { splitUpdateWarnings, summarizeUpdateWarnings } from './api';
import type { UpdateBookResult } from './api';

describe('update warnings', () => {
  it('splits the warnings off the saved book', () => {
    const saved = { id: 'b1', title: 'T', warnings: ['w1'] } as unknown as UpdateBookResult;
    const { book, warnings } = splitUpdateWarnings(saved);
    expect(warnings).toEqual(['w1']);
    expect('warnings' in book).toBe(false);
    expect(book.id).toBe('b1');
  });

  it('summarizes only the books that have warnings', () => {
    expect(
      summarizeUpdateWarnings([
        { label: 'A', warnings: [] },
        { label: 'B', warnings: ['history not recorded', 'locks not saved'] },
        { label: 'C', warnings: ['narrators not updated'] },
      ])
    ).toEqual({
      count: 2,
      text: 'B: history not recorded; locks not saved | C: narrators not updated',
    });
  });

  it('is null when no book has a warning', () => {
    expect(summarizeUpdateWarnings([{ label: 'A', warnings: [] }])).toBeNull();
    expect(summarizeUpdateWarnings([])).toBeNull();
  });
});
