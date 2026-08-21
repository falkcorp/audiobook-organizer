// file: web/src/components/review/evidence/signalLabels.test.ts
// version: 1.0.0
// guid: 1f7a4c93-8b26-4d50-91ce-6a04b8d3e527
// last-edited: 2026-08-20

import { describe, it, expect } from 'vitest';
import type { DedupScoreBreakdown } from '../../../services/api';
import { primarySignals, signalLabel } from './signalLabels';

function breakdown(
  signals: Array<{ kind: string; primary?: boolean }>
): DedupScoreBreakdown {
  return {
    score: 90,
    signals: signals.map((s) => ({
      kind: s.kind,
      value: 1,
      weight: 1,
      primary: s.primary ?? false,
    })),
  } as unknown as DedupScoreBreakdown;
}

describe('signalLabel', () => {
  it('names the kinds the scorer emits', () => {
    expect(signalLabel('exact_file')).toBe('exact file');
    expect(signalLabel('isbn_asin')).toBe('ISBN/ASIN');
    expect(signalLabel('metadata_hash')).toBe('same source record');
  });

  it('falls through to the raw kind for one it does not know', () => {
    // A new collector must render its own name rather than an empty chip. The
    // map is a readability layer, never a gate on what can be displayed.
    expect(signalLabel('some_future_collector')).toBe('some_future_collector');
  });
});

describe('primarySignals', () => {
  it('returns primary signals in the order the scorer recorded them', () => {
    const out = primarySignals(
      breakdown([
        { kind: 'exact_file', primary: true },
        { kind: 'isbn_asin', primary: true },
      ])
    );
    expect(out.map((s) => s.kind)).toEqual(['exact_file', 'isbn_asin']);
    expect(out.map((s) => s.label)).toEqual(['exact file', 'ISBN/ASIN']);
  });

  it('omits supporting signals', () => {
    // Not an economy. score.go excludes supporting signals from the noisy-OR
    // product and states a supporting-only set can never reach an eligible
    // score -- so duration can corroborate a pair but never be the reason one
    // exists. A chip beside the primaries would claim otherwise.
    const out = primarySignals(
      breakdown([
        { kind: 'exact_file', primary: true },
        { kind: 'duration' },
        { kind: 'folder_path' },
      ])
    );
    expect(out.map((s) => s.kind)).toEqual(['exact_file']);
  });

  it('is empty rather than throwing when there is no breakdown', () => {
    // Rows predating the scorer carry no breakdown at all; the row still has to
    // render.
    expect(primarySignals(null)).toEqual([]);
    expect(primarySignals(undefined)).toEqual([]);
    expect(primarySignals({ score: 0 } as unknown as DedupScoreBreakdown)).toEqual([]);
  });

  it('is empty when every signal is supporting', () => {
    expect(primarySignals(breakdown([{ kind: 'duration' }]))).toEqual([]);
  });
});
