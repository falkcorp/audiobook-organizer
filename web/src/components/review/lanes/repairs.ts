// file: web/src/components/review/lanes/repairs.ts
// version: 1.1.0
// guid: 4d7a2e91-6c38-4f05-b1e7-9a3c5f0d8b26
// last-edited: 2026-10-06

import type { LaneDescriptor } from './types';

/**
 * The repairs lane: library fixers, one trial (plan) at a time.
 *
 * Its evidence is facts, like the review queue: a fixer proposes a row because
 * of rules over the stored state ("two members flagged primary, this one is the
 * organized copy"), not because of a score, so there is nothing to draw a bar
 * from.
 *
 * "Run trial" rather than "plan" or "dry run": the trial writes nothing, and the
 * reviewer then picks from what it found. "Apply" here always writes -- the
 * lane never sends the server's preview mode.
 */
export const repairsLane = {
  lane: 'repairs',
  label: 'Repairs',
  evidenceKind: 'facts',
  verbs: {
    runTrial: 'Run trial',
    applyRows: 'Apply selected',
    applyAllApplicable: 'Apply all applicable',
    ownerApplyRow: 'Apply (owner)',
  },
  emptyMessage: 'No repair fixers are registered on this server.',
} satisfies LaneDescriptor<'repairs'>;
