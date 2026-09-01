// file: web/src/components/review/evidence/signalLabels.test.ts
// version: 2.0.0
// guid: 1f7a4c93-8b26-4d50-91ce-6a04b8d3e527
// last-edited: 2026-09-01

import { describe, it, expect } from 'vitest';
import type { DedupScoreBreakdown, DedupSignal } from '../../../services/api';
import { isPrimaryKind, primarySignals, signalLabel } from './signalLabels';

/**
 * Builds a breakdown from real `DedupSignal`s -- no cast.
 *
 * The helper this replaces built `{kind, value, weight, primary}` behind an
 * `as unknown as DedupScoreBreakdown`, which is a shape the backend has never
 * emitted. The cast is what let the fiction survive: it silenced the one check
 * that would have caught the wire-format mismatch this file exists to guard.
 */
function breakdown(signals: Array<Partial<DedupSignal> & { kind: string }>): DedupScoreBreakdown {
  return {
    score: 90,
    band: 'HIGH',
    formula: 'v2',
    signals: signals.map((s) => ({
      kind: s.kind,
      raw: s.raw ?? 1,
      confidence: s.confidence ?? 0.9,
      evidence: s.evidence ?? `${s.kind} fired`,
    })),
  };
}

describe('signalLabel', () => {
  it('names the kinds the scorer emits', () => {
    expect(signalLabel('exact_file')).toBe('Exact file hash');
    expect(signalLabel('isbn_asin')).toBe('ISBN/ASIN');
    expect(signalLabel('metadata_hash')).toBe('Metadata hash');
  });

  it('falls through to the raw kind for one it does not know', () => {
    // A new collector must render its own name rather than an empty chip. The
    // map is a readability layer, never a gate on what can be displayed.
    expect(signalLabel('some_future_collector')).toBe('some_future_collector');
  });
});

describe('isPrimaryKind', () => {
  // The rule is duplicated from isSupportingKind in
  // internal/dedup/unified/score.go because the wire format does not carry the
  // classification. These assertions name every kind the scorer emits, so a
  // kind moving between the two groups in Go fails here rather than silently
  // changing what the chips claim.
  it('treats exactly duration and folder_path as supporting', () => {
    expect(isPrimaryKind('duration')).toBe(false);
    expect(isPrimaryKind('folder_path')).toBe(false);
  });

  it('treats every other emitted kind as primary', () => {
    for (const kind of [
      'exact_file',
      'exact_acoustid',
      'isbn_asin',
      'lsh_acoustid',
      'embedding_high',
      'metadata_hash',
      'metadata_fuzzy',
      'embedding_med',
    ]) {
      expect(isPrimaryKind(kind), `${kind} should be primary`).toBe(true);
    }
  });

  it('treats an unknown kind as primary rather than dropping it', () => {
    // The safe direction: a new collector shows up in the chips and the panel
    // instead of vanishing from both.
    expect(isPrimaryKind('some_future_collector')).toBe(true);
  });
});

describe('primarySignals', () => {
  it('returns primary signals in the order the scorer recorded them', () => {
    const out = primarySignals(breakdown([{ kind: 'exact_file' }, { kind: 'isbn_asin' }]));
    expect(out.map((s) => s.kind)).toEqual(['exact_file', 'isbn_asin']);
    expect(out.map((s) => s.label)).toEqual(['Exact file hash', 'ISBN/ASIN']);
  });

  it('omits supporting signals', () => {
    // Not an economy. score.go excludes supporting signals from the noisy-OR
    // product and states a supporting-only set can never reach an eligible
    // score -- so duration can corroborate a pair but never be the reason one
    // exists. A chip beside the primaries would claim otherwise.
    //
    // The fixture mixes both groups on purpose: a supporting-only or
    // primary-only fixture cannot tell a correct filter from an inverted one.
    const out = primarySignals(
      breakdown([{ kind: 'exact_file' }, { kind: 'duration' }, { kind: 'folder_path' }])
    );
    expect(out.map((s) => s.kind)).toEqual(['exact_file']);
  });

  it('is empty rather than throwing when there is no breakdown', () => {
    // Rows predating the scorer carry no breakdown at all; the row still has to
    // render.
    expect(primarySignals(null)).toEqual([]);
    expect(primarySignals(undefined)).toEqual([]);
    // This cast is deliberate and stays: it fabricates a MALFORMED payload (a
    // breakdown with no `signals` key at all) to prove the guard holds. It is
    // not standing in for the real wire shape.
    expect(primarySignals({ score: 0 } as unknown as DedupScoreBreakdown)).toEqual([]);
  });

  it('is empty when every signal is supporting', () => {
    expect(primarySignals(breakdown([{ kind: 'duration' }]))).toEqual([]);
  });
});
