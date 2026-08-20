// file: web/src/components/review/evidence/types.test.ts
// version: 1.1.0
// guid: 1d7c4b28-93ef-4a05-b6c1-8e2f0a5d9c34
// last-edited: 2026-08-20

import { describe, expect, it } from 'vitest';
import {
  recomposeWaterfall,
  waterfallIsConsistent,
  type WaterfallEvidence,
  type WaterfallStep,
} from './types';

// These helpers are the reason the metadata lane gets a waterfall instead of a
// share bar: they turn "the breakdown explains the score" from an assumption
// into a checkable property. If they are wrong, a breakdown that does NOT
// derive the score would still be presented as if it did.

describe('recomposeWaterfall', () => {
  it('replays the real metadata pipeline shape', () => {
    // (base * compilationPenalty * lengthPenalty) + richMetadataBonus
    const steps: WaterfallStep[] = [
      { id: 'base', label: 'Title/author match', op: 'base', operand: 0.8, running: 0.8 },
      { id: 'comp', label: 'Compilation penalty', op: 'multiply', operand: 0.5, running: 0.4 },
      { id: 'len', label: 'Length penalty', op: 'multiply', operand: 0.75, running: 0.3 },
      { id: 'rich', label: 'Rich metadata', op: 'add', operand: 0.1, running: 0.4 },
    ];
    expect(recomposeWaterfall(steps)).toBeCloseTo(0.4, 12);
  });

  it('is order-sensitive, because the arithmetic is', () => {
    // Multiplying before vs after an addition gives different totals. A share
    // bar cannot express this distinction at all -- which is precisely why
    // metadata evidence must not be shaped as weights.
    const base: WaterfallStep = { id: 'b', label: 'base', op: 'base', operand: 1, running: 1 };
    const add: WaterfallStep = { id: 'a', label: 'add', op: 'add', operand: 1, running: 2 };
    const mul: WaterfallStep = { id: 'm', label: 'mul', op: 'multiply', operand: 2, running: 2 };

    expect(recomposeWaterfall([base, add, mul])).toBe(4); // (1+1)*2
    expect(recomposeWaterfall([base, mul, add])).toBe(3); // (1*2)+1
  });

  it('discards the prior total on a replace', () => {
    // Rerank and direct-ASIN matches substitute a verdict. Anything the
    // pipeline computed before that point no longer contributes.
    const steps: WaterfallStep[] = [
      { id: 'base', label: 'base', op: 'base', operand: 0.5, running: 0.5 },
      { id: 'x', label: 'x', op: 'multiply', operand: 4, running: 2 },
      { id: 'r', label: 'rerank', op: 'replace', operand: 0.3, running: 0.3 },
      { id: 'y', label: 'y', op: 'add', operand: 0.1, running: 0.4 },
    ];
    expect(recomposeWaterfall(steps)).toBeCloseTo(0.4, 12);
  });

  it('returns 0 for no steps', () => {
    expect(recomposeWaterfall([])).toBe(0);
  });

  it('ignores each step’s stored running total', () => {
    // Recomputing from `operand` is what makes the check meaningful. If a
    // backend emitted a wrong `running`, recompose must still report the truth.
    const steps: WaterfallStep[] = [
      { id: 'base', label: 'base', op: 'base', operand: 0.5, running: 999 },
      { id: 'x', label: 'x', op: 'multiply', operand: 2, running: -1 },
    ];
    expect(recomposeWaterfall(steps)).toBe(1);
  });
});

describe('waterfallIsConsistent', () => {
  const steps: WaterfallStep[] = [
    { id: 'base', label: 'base', op: 'base', operand: 0.9, running: 0.9 },
    { id: 'p', label: 'penalty', op: 'multiply', operand: 0.5, running: 0.45 },
  ];

  it('accepts a breakdown that reproduces its score', () => {
    const ev: WaterfallEvidence = { kind: 'waterfall', score: 0.45, steps };
    expect(waterfallIsConsistent(ev)).toBe(true);
  });

  it('rejects a breakdown that does not', () => {
    // The failure this exists to catch: a backend that returns the components
    // it happens to have retained rather than the ones that made the number.
    const ev: WaterfallEvidence = { kind: 'waterfall', score: 0.72, steps };
    expect(waterfallIsConsistent(ev)).toBe(false);
  });

  it('tolerates float drift but not real divergence', () => {
    const ev: WaterfallEvidence = { kind: 'waterfall', score: 0.45 + 1e-12, steps };
    expect(waterfallIsConsistent(ev)).toBe(true);
    expect(waterfallIsConsistent({ ...ev, score: 0.4501 })).toBe(false);
  });

  it('rejects an empty breakdown rather than calling it consistent with 0', () => {
    // score 0 with no steps is "we recorded nothing", not "we proved zero".
    const ev: WaterfallEvidence = { kind: 'waterfall', score: 0, steps: [] };
    expect(waterfallIsConsistent(ev)).toBe(false);
  });
});
