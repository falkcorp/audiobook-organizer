// file: web/src/components/review/evidence/adapters.ts
// version: 1.0.0
// guid: e21a8c47-3f60-4b95-8d1e-7a4c0b6f2953
// last-edited: 2026-08-20
//
// Lane payload -> Evidence. One adapter per lane, each choosing the evidence
// kind that matches how that lane's number was actually computed.
//
// These are deliberately thin and total: every branch returns an Evidence, and
// an absent or empty payload produces an `emptyReason` rather than null, so the
// panel can say WHY it has nothing to show instead of rendering a blank box. A
// reviewer who cannot tell "no evidence was recorded" from "the evidence was
// weak" cannot use the panel for the thing it exists for.

import type { DedupScoreBreakdown, MetadataCandidate } from '../../../services/api';
import type { RecommendationEvidence } from '../../../lib/reviewPayload';
import { evidenceFacts } from '../../../lib/reviewPayload';
import type { Evidence, FactsEvidence, WaterfallEvidence, WeightedEvidence } from './types';

/**
 * Dedup -> weighted. This lane's score genuinely IS a weighted sum, which is
 * why it keeps the stacked contribution bar the other two cannot have.
 */
export function dedupEvidence(breakdown: DedupScoreBreakdown | null | undefined): WeightedEvidence {
  if (!breakdown) {
    return { kind: 'weighted', score: 0, signals: [], emptyReason: 'No score breakdown recorded.' };
  }
  return {
    kind: 'weighted',
    score: breakdown.score,
    band: breakdown.band,
    formula: breakdown.formula,
    emptyReason: breakdown.skipped_reason,
    signals: (breakdown.signals ?? []).map((s) => ({
      id: s.kind,
      label: s.kind,
      value: s.value,
      weight: s.weight,
      detail: s.evidence,
      primary: s.primary,
    })),
  };
}

/**
 * Regroup -> facts. There is no score here at all: a recommendation is reached
 * by rules over these counts, not by arithmetic on them, so there is nothing to
 * draw a bar from and any bar drawn would be invented.
 *
 * The fact shaping itself is NOT reimplemented -- `evidenceFacts` has produced
 * these rows, with their tooltips and their warn flag for the known-runtime gap,
 * since 2026-08-06 and is covered by its own tests.
 */
export function regroupEvidence(
  ev: RecommendationEvidence | null | undefined,
  reason?: string
): FactsEvidence {
  const facts = evidenceFacts(ev ?? undefined);
  return {
    kind: 'facts',
    headline: reason,
    facts,
    emptyReason: facts.length === 0 ? 'No evidence recorded for this hold.' : undefined,
  };
}

/**
 * Metadata -> waterfall. The pipeline is `(base × factors) + terms` and can be
 * replaced outright by an LLM rerank or a direct ASIN match, so the only
 * faithful rendering is an ordered replay. See docs/evidence-panel-audit.md for
 * why this must not be reshaped into weights.
 *
 * A candidate with no `score_breakdown` came from a path that records no
 * derivation. That is reported as such -- never as a zero score, and never by
 * synthesising steps from the summary fields (`duration_score` and friends),
 * which are signal summaries rather than contributions and would not replay to
 * the score.
 */
export function metadataEvidence(
  candidate: MetadataCandidate | null | undefined
): WaterfallEvidence {
  if (!candidate) {
    return { kind: 'waterfall', score: 0, steps: [], emptyReason: 'No candidate selected.' };
  }
  const breakdown = candidate.score_breakdown;
  if (!breakdown || breakdown.steps.length === 0) {
    return {
      kind: 'waterfall',
      score: candidate.score,
      steps: [],
      emptyReason:
        'This candidate was produced without a recorded derivation, so its score cannot be explained here.',
    };
  }
  return {
    kind: 'waterfall',
    score: breakdown.score,
    steps: breakdown.steps.map((s) => ({
      id: s.id,
      label: s.label,
      op: s.op,
      operand: s.operand,
      running: s.running,
      detail: s.detail,
      capped: s.capped,
    })),
  };
}

/** Narrowing helper for callers holding an Evidence of unknown kind. */
export function isWeighted(e: Evidence): e is WeightedEvidence {
  return e.kind === 'weighted';
}
