// file: web/src/components/review/repairs/rowHelpers.mergeCleanup.test.ts
// version: 1.1.0
// guid: 6b0e3c18-7f42-4d95-8a61-c2d4f9e07b35
// last-edited: 2026-11-18

/**
 * The merge-cleanup preview lists three row classes, a "no verdict" class and
 * ten flags. A chip reading "repoint_chain (3)" tells the owner nothing, so
 * each needs words and no two may read alike.
 */
import { describe, expect, it } from 'vitest';
import { classLabel, flagLabel } from './rowHelpers';

describe('merge-cleanup labels', () => {
  const classes = ['unmerge', 'swap_keeper', 'repoint_chain', 'no_verdict'];
  const flags = [
    'chain_via_merged',
    'chain_via_trashed',
    'group_needs_handoff',
    'sync_redirect_present',
    'owner_says_distinct',
    'origin_match4',
    'origin_match4_partial',
    'origin_other',
    'origin_unknown',
    'sync_redirect_broken',
  ];

  it('words every class', () => {
    for (const c of classes) expect(classLabel(c), c).not.toBe(c);
    expect(new Set(classes.map(classLabel)).size).toBe(classes.length);
  });

  it('words every flag', () => {
    for (const f of flags) expect(flagLabel(f), f).not.toBe(f);
    expect(new Set(flags.map(flagLabel)).size).toBe(flags.length);
  });
});
