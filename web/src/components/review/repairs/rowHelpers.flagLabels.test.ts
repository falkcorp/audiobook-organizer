// file: web/src/components/review/repairs/rowHelpers.flagLabels.test.ts
// version: 1.0.1
// guid: 9d2b6f41-8e07-4c35-b1a8-3f5e0c7a4d62
// last-edited: 2026-11-04

/**
 * A fixer's row flags (Row.flags) show as a chip with words, not the flag id.
 */
import { describe, expect, it } from 'vitest';
import { flagFilter, flagLabel } from './rowHelpers';

describe('flagLabel', () => {
  it('has words for a known flag', () => {
    expect(flagLabel('combine_undo_aware')).not.toBe('combine_undo_aware');
  });

  it('shows an unknown flag as itself and pages a flag by its own filter', () => {
    expect(flagLabel('some_future_flag')).toBe('some_future_flag');
    expect(flagFilter('combine_undo_aware')).toBe('flag:combine_undo_aware');
  });
});
