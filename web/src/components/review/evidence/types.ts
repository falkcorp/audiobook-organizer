// file: web/src/components/review/evidence/types.ts
// version: 1.0.0
// guid: 8b3f1a94-6c02-4e7d-95a1-2f8e4d0c7b63
// last-edited: 2026-08-20
//
// The evidence model behind the unified EvidencePanel.
//
// Phase 5 promotes the dedup lane's ScoreBreakdownPanel to a panel shared by all
// three lanes. The naive version of that is "one shape with optional fields",
// and it produces a specific, hard-to-notice lie.
//
// ScoreBreakdownPanel draws a stacked bar whose segment widths are
// `weight / sum(weights)` -- a SHARE OF A TOTAL. That encoding asserts "these
// parts sum to the whole". The assertion is true for dedup, whose score is a
// weighted sum. It is false for metadata, whose score is
//
//     (base * compilationPenalty * lengthPenalty) + richMetadataBonus
//
// A multiplicative factor has no share of a total. Feeding one to that bar
// produces segments that sum to nothing meaningful -- which is worse than
// showing no bar at all, because it still looks complete. See
// docs/evidence-panel-audit.md.
//
// So evidence is a discriminated union over how the number was actually
// computed, and each kind gets the rendering its arithmetic supports:
//
//   weighted  -- score is a weighted sum      -> stacked share bar
//   facts     -- named observations, no score -> fact rows, no bar
//   waterfall -- ordered ops on a running total -> waterfall rows
//
// Adding a lane means picking the kind that matches its arithmetic, never
// bending the arithmetic to fit a kind that already renders nicely.

/** Discriminator: how the underlying number was computed. */
export type EvidenceKind = 'weighted' | 'facts' | 'waterfall';

// ---------------------------------------------------------------------------
// weighted -- dedup
// ---------------------------------------------------------------------------

export interface WeightedSignal {
  /** Stable key: dedup signal `kind`, used for colour and React keys. */
  id: string;
  label: string;
  /** Raw signal strength, 0-1. */
  value: number;
  /** Calibration weight. Negative weights are clamped to 0 for the bar. */
  weight: number;
  /** Human-readable justification, shown on hover. */
  detail?: string;
  /** Whether this signal alone is sufficient to call a duplicate. */
  primary?: boolean;
}

export interface WeightedEvidence {
  kind: 'weighted';
  score: number;
  /** Band label (CERTAIN | HIGH | MEDIUM | REVIEW) when the lane has one. */
  band?: string;
  /** Scoring algorithm version tag. */
  formula?: string;
  signals: WeightedSignal[];
  /** Why there are no signals, when the list is empty. */
  emptyReason?: string;
}

// ---------------------------------------------------------------------------
// facts -- regroup
// ---------------------------------------------------------------------------

/**
 * A named observation with no weight and no contribution to a score. Regroup
 * recommendations are reached by rules over these counts rather than by
 * arithmetic on them, so there is nothing to draw a bar from.
 *
 * This is the shape `evidenceFacts` in lib/reviewPayload.ts has produced since
 * 2026-08-06; it lives here now so the generic evidence model does not depend on
 * a regroup-specific payload parser. `label` carries the whole rendered phrase
 * ("3/12 runtimes known") because these render as chips, not as an aligned
 * table -- there is no separate numeric column to right-align.
 */
export interface EvidenceFact {
  label: string;
  /** Longer explanation for a tooltip -- what the number means, not just its name. */
  hint: string;
  /** True when this fact is the reason a recommendation could not be decisive. */
  warn?: boolean;
}

export interface FactsEvidence {
  kind: 'facts';
  /** The recommendation or verdict these facts support. */
  headline?: string;
  facts: EvidenceFact[];
  emptyReason?: string;
}

// ---------------------------------------------------------------------------
// waterfall -- metadata
// ---------------------------------------------------------------------------

/**
 * One step in a running-total pipeline.
 *
 * `running` is the total AFTER applying this step, which is what makes the
 * whole structure verifiable: replaying the ops from the base must reproduce
 * the shipped score. See `recomposeWaterfall`.
 */
export interface WaterfallStep {
  id: string;
  label: string;
  op: 'base' | 'multiply' | 'add';
  /** The base value, the multiplier, or the addend, depending on `op`. */
  operand: number;
  /** Running total after this step. */
  running: number;
  detail?: string;
  /** Set when the operand was clamped by a cap (e.g. RichMetadataBonusCap). */
  capped?: boolean;
}

export interface WaterfallEvidence {
  kind: 'waterfall';
  /** The score as actually shipped. `recomposeWaterfall` must reproduce it. */
  score: number;
  steps: WaterfallStep[];
  emptyReason?: string;
}

export type Evidence = WeightedEvidence | FactsEvidence | WaterfallEvidence;

// ---------------------------------------------------------------------------
// Verification
// ---------------------------------------------------------------------------

/**
 * Replay a waterfall from its first step and return the final total.
 *
 * This exists so "the breakdown is a decomposition of the score" is a property
 * that can be TESTED rather than an assumption. If `recomposeWaterfall(ev.steps)`
 * does not equal `ev.score`, the backend is emitting annotations that merely sit
 * near the score -- not the arithmetic that produced it -- and the panel must not
 * present them as a derivation.
 *
 * Returns 0 for an empty step list. Ignores each step's own `running` field by
 * design: recomputing from `operand` is what makes the check meaningful.
 */
export function recomposeWaterfall(steps: WaterfallStep[]): number {
  let total = 0;
  for (const step of steps) {
    switch (step.op) {
      case 'base':
        total = step.operand;
        break;
      case 'multiply':
        total *= step.operand;
        break;
      case 'add':
        total += step.operand;
        break;
    }
  }
  return total;
}

/**
 * Whether a waterfall's steps reproduce its score, within floating-point
 * tolerance. `epsilon` is absolute; scores here live in [0, ~1.2].
 */
export function waterfallIsConsistent(ev: WaterfallEvidence, epsilon = 1e-9): boolean {
  if (ev.steps.length === 0) return false;
  return Math.abs(recomposeWaterfall(ev.steps) - ev.score) <= epsilon;
}
