// file: web/src/utils/searchParser.test.ts
// version: 1.0.0
// guid: 3c7e9a15-6d2f-4b84-a0e3-9f1b5c8d2e47
// last-edited: 2026-09-27

import { describe, it, expect } from 'vitest';
import { parseSearch } from './searchParser';

describe('parseSearch: metadata backlog filters', () => {
  it('turns the owner query into two server-side field filters', () => {
    const parsed = parseSearch('-metadata:applied duration:>20m');
    expect(parsed.freeText).toBe('');
    expect(parsed.fieldFilters).toEqual([
      { field: 'metadata', value: 'applied', negated: true, quoted: false },
      { field: 'duration', value: '>20m', negated: false, quoted: false },
    ]);
  });

  it('keeps a bracketed range with spaces as one value', () => {
    const parsed = parseSearch('duration:[10m TO 2h] dune');
    expect(parsed.fieldFilters).toEqual([
      { field: 'duration', value: '[10m TO 2h]', negated: false, quoted: false },
    ]);
    expect(parsed.freeText).toBe('dune');
  });

  it('recognises has_duration', () => {
    expect(parseSearch('has_duration:no').fieldFilters[0]).toMatchObject({
      field: 'has_duration',
      value: 'no',
    });
  });
});
