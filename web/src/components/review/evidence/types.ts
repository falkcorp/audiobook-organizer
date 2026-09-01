// file: web/src/components/review/evidence/types.ts
// version: 2.0.0
// guid: 8b3f1a94-6c02-4e7d-95a1-2f8e4d0c7b63
// last-edited: 2026-09-01
//
// The evidence model behind the unified EvidencePanel.
//
// Phase 5 promotes the dedup lane's ScoreBreakdownPanel to a panel shared by all
// three lanes. The naive version of that is "one shape with optional fields",
// and it produces a specific, hard-to-notice lie.
//
// ScoreBreakdownPanel drew a stacked bar whose segment widths were
// `weight / sum(weights)` -- a SHARE OF A TOTAL. That encoding asserts "these
// parts sum to the whole". It is false for metadata, whose score is
//
//     (base * compilationPenalty * lengthPenalty) + richMetadataBonus
//
// A multiplicative factor has no share of a total. Feeding one to that bar
// produces segments that sum to nothing meaningful -- which is worse than
// showing no bar at all, because it still looks complete. See
// docs/evidence-panel-audit.md.
//
// IT WAS ALSO FALSE FOR DEDUP, which is why the bar is gone entirely as of
// 2026-09-01. This file used to assert "the assertion is true for dedup, whose
// score is a weighted sum". ComposeScore (internal/dedup/unified/compose.go)
// computes
//
//     100 * (1 - PROD(1 - confidence_i)) + SUM(boost_j)      capped at 100
//
// -- a noisy-OR product over the primary signals plus bounded additive boosts
// from the two supporting ones. There are no weights anywhere in it: ScoreConfig
// carries a per-kind `Confidence` and a `Boost` for the supporting kinds only.
// A product has no decomposition into shares, so the bar was asserting of dedup
// exactly what docs/evidence-panel-audit.md rejected a bar for on the metadata
// lane. The signals are still each other's context, but they are not each
// other's fractions, and they are rendered as independent confidences.
//
// So evidence is a discriminated union over how the number was actually
// computed, and each kind gets the rendering its arithmetic supports:
//
//   confidence -- independent per-signal probabilities -> confidence rows, no bar
//   facts      -- named observations, no score          -> fact rows, no bar
//   waterfall  -- ordered ops on a running total        -> waterfall rows
//
// Adding a lane means picking the kind that matches its arithmetic, never
// bending the arithmetic to fit a kind that already renders nicely.

/** Discriminator: how the underlying number was computed. */
export type EvidenceKind = 'confidence' | 'facts' | 'waterfall';

// ---------------------------------------------------------------------------
// confidence -- dedup
// ---------------------------------------------------------------------------

/**
 * One signal's contribution to a noisy-OR score.
 *
 * `confidence` is the number the scorer actually consumes -- models.Signal in
 * internal/models/dedup_score.go says so outright: "ComposeScore reads this
 * field; Raw is stored for human auditing and re-calibration". So confidence is
 * the headline and `raw` is context, never the other way round.
 *
 * There is deliberately no `weight`. Nothing weights these signals; see the
 * formula at the top of this file.
 */
export interface ConfidenceSignal {
  /** Stable key: dedup signal `kind`, used for colour and React keys. */
  id: string;
  label: string;
  /** Calibrated P(duplicate | this signal alone), 0-1. Drives the score. */
  confidence: number;
  /** The unscaled measurement behind `confidence` (cosine, Hamming, ...). */
  raw: number;
  /** Human-readable justification, shown on hover. */
  detail?: string;
  /**
   * Whether this signal alone can be the reason a pair exists. Re-derived from
   * `kind` by signalLabels.isPrimaryKind -- the wire format does not carry it.
   */
  primary?: boolean;
}

export interface ConfidenceEvidence {
  kind: 'confidence';
  score: number;
  /** Band label (CERTAIN | HIGH | MEDIUM | REVIEW) when the lane has one. */
  band?: string;
  /** Scoring algorithm version tag. */
  formula?: string;
  signals: ConfidenceSignal[];
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
  /**
   * `replace` overwrites the running total instead of adjusting it. The LLM
   * reranker and a direct ASIN match both do this -- they substitute a verdict
   * rather than scaling the evidence. Expressing either as a multiply by
   * new/old would recompose correctly while showing the reviewer a factor that
   * corresponds to no real signal.
   */
  op: 'base' | 'multiply' | 'add' | 'replace';
  /** The base value, the multiplier, the addend, or the replacement, per `op`. */
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

export type Evidence = ConfidenceEvidence | FactsEvidence | WaterfallEvidence;

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
      case 'replace':
        total = step.operand;
        break;
    }
  }
  return total;
}

/**
 * Whether a waterfall's steps reproduce its score, within floating-point
 * tolerance. `epsilon` is absolute; scores here live in [0, ~1.2].
 *
 * Non-finite values are inconsistent, and this must be checked explicitly
 * rather than left to the comparison. `NaN` is unordered against everything,
 * so `Math.abs(NaN - score) > epsilon` is FALSE -- a caller phrasing the test
 * as "flag it when they diverge" would report agreement. That is not a
 * hypothetical: a wire-format drift (a renamed JSON tag, a field the backend
 * stopped sending) yields steps whose `operand` is `undefined`, which recomposes
 * to `NaN`, and the panel would then present rows it could not verify as a
 * verified derivation. Requiring finiteness makes the seam fail loudly.
 */
export function waterfallIsConsistent(ev: WaterfallEvidence, epsilon = 1e-9): boolean {
  if (ev.steps.length === 0) return false;
  const recomposed = recomposeWaterfall(ev.steps);
  if (!Number.isFinite(recomposed) || !Number.isFinite(ev.score)) return false;
  return Math.abs(recomposed - ev.score) <= epsilon;
}

/** Fixed-digit formatting that tolerates a value the wire did not supply. */
function fmt(value: number, digits: number): string {
  return Number.isFinite(value) ? value.toFixed(digits) : '—';
}

/**
 * Why a breakdown is being shown as incomplete.
 *
 * The two causes are genuinely different and point at different fixes, so they
 * do not get the same sentence: a breakdown that replays to the WRONG number
 * means the scorer and the recorder disagree, and one that cannot be replayed
 * AT ALL means the payload and this panel disagree about the shape of a step.
 * Lives here, beside the predicate it explains, rather than in the panel:
 * the panel is a component module, and exporting a plain function from one
 * breaks Fast Refresh for the whole file.
 */
export function incompleteReason(recomposed: number, score: number): string {
  if (!Number.isFinite(recomposed)) {
    return (
      'These steps cannot be replayed at all -- at least one is missing the number it operates on. ' +
      'That usually means the payload and this panel disagree about the shape of a step, so nothing ' +
      'here should be read as a derivation of the score.'
    );
  }
  return (
    `These steps replay to ${fmt(recomposed, 4)}, not ${fmt(score, 4)}. ` +
    'The breakdown does not explain this score, so treat it as incomplete rather than as a derivation.'
  );
}
