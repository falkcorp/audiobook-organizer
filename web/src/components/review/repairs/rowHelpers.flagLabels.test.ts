// file: web/src/components/review/repairs/rowHelpers.flagLabels.test.ts
// version: 1.0.0
// guid: 9d2b6f41-8e07-4c35-b1a8-3f5e0c7a4d62
// last-edited: 2026-11-03

/**
 * The folder-books plan counts single-shelf rows held only by checks the owner
 * could resolve (flag single_shelf_owner_resolvable). The chip that shows that
 * count needs words, not the flag id.
 */
import { describe, expect, it } from 'vitest';
import { flagFilter, flagLabel } from './rowHelpers';

describe('flagLabel', () => {
  it('has words for the single-shelf measurement flag', () => {
    expect(flagLabel('single_shelf_owner_resolvable')).not.toBe('single_shelf_owner_resolvable');
  });

  it('shows an unknown flag as itself and pages a flag by its own filter', () => {
    expect(flagLabel('some_future_flag')).toBe('some_future_flag');
    expect(flagFilter('single_shelf_owner_resolvable')).toBe('flag:single_shelf_owner_resolvable');
  });
});
