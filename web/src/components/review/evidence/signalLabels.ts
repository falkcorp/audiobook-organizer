// file: web/src/components/review/evidence/signalLabels.ts
// version: 2.0.0
// guid: 6d8b3f51-2a94-4c07-ae63-5f10c7d92b48
// last-edited: 2026-09-01
//
// Reviewer-facing names for dedup signal kinds, and the primary/supporting
// rule, so the dupes rows can say WHY a pair is on screen without the reviewer
// expanding anything.
//
// This exists because of what the triage set actually looks like: no CERTAIN
// pair rests on a fuzzy title match -- every one is backed by exact_file,
// isbn_asin or metadata_hash -- so the reviewer is never adjudicating a
// resemblance. They are confirming a named signal. That is a one-line read, and
// making them open a panel for it is the round-trip worth removing.

import type { DedupScoreBreakdown } from '../../../services/api';

/**
 * Kind -> reviewer-facing label. THE map: the evidence panel, the row chips and
 * the compare drawer all import this one, so a signal cannot be called three
 * different things on three surfaces. It was three separate copies until
 * 2026-09-01, and they had already drifted -- the row chip said "exact file"
 * where the panel said "Exact file hash".
 *
 * Deliberately close to the Go constants in internal/dedup/unified/score.go
 * rather than freely reworded: these are the names that appear in the config's
 * per-kind overrides and in the logs, and a chip that calls a signal something
 * no other surface calls it costs more than the raw string would. Unknown kinds
 * fall through to the raw kind (see `signalLabel`), so a new collector renders
 * its own name instead of nothing.
 */
export const SIGNAL_LABELS: Record<string, string> = {
  exact_file: 'Exact file hash',
  exact_acoustid: 'Exact AcoustID',
  isbn_asin: 'ISBN/ASIN',
  lsh_acoustid: 'LSH AcoustID',
  embedding_high: 'Embedding (high)',
  metadata_hash: 'Metadata hash',
  metadata_fuzzy: 'Metadata fuzzy',
  embedding_med: 'Embedding (medium)',
  duration: 'Duration match',
  folder_path: 'Folder path',
};

export function signalLabel(kind: string): string {
  return SIGNAL_LABELS[kind] ?? kind;
}

/**
 * The SUPPORTING signal kinds: excluded from the noisy-OR product, contributing
 * only a bounded additive boost after it.
 *
 * SOURCE OF TRUTH IS GO: `isSupportingKind` in internal/dedup/unified/score.go
 * (`k == SigDuration || k == SigFolderPath`). This is a DUPLICATE of that rule,
 * and duplicating it is not the design -- it is a stopgap. The wire format
 * (models.Signal in internal/models/dedup_score.go) does not serialize the
 * primary/supporting classification at all, so the frontend cannot read the
 * answer and has to re-derive it. A follow-up PR should add a `primary` field to
 * models.Signal and make this list collapse into reading it.
 *
 * Until then: if a kind is added to `isSupportingKind` in Go and not here, its
 * chip silently starts claiming to be the reason a pair exists.
 */
const SUPPORTING_KINDS: ReadonlySet<string> = new Set(['duration', 'folder_path']);

/**
 * Whether a signal kind can, on its own, be the reason a pair is a candidate.
 *
 * Note the polarity: everything the scorer emits is primary EXCEPT the two
 * supporting kinds. An unknown kind from a new collector is therefore treated as
 * primary, which is the safe direction -- it appears in the panel and the chips
 * rather than being silently dropped from both.
 */
export function isPrimaryKind(kind: string): boolean {
  return !SUPPORTING_KINDS.has(kind);
}

export interface PrimarySignal {
  kind: string;
  label: string;
}

/**
 * The primary signals behind a candidate, in the order the scorer recorded
 * them. Empty when there is no breakdown or every signal is supporting.
 *
 * SUPPORTING SIGNALS ARE OMITTED, and that is the point rather than an
 * economy. score.go excludes them from the noisy-OR product and states that a
 * set of supporting-only signals can never reach an eligible score -- so
 * `duration` and `folder_path` can corroborate a pair but can never be the
 * reason one exists. Rendering them beside the primaries would give them equal
 * visual weight in the exact glance this is meant to make reliable. The full
 * set, supporting included, is still in the evidence panel.
 */
export function primarySignals(
  breakdown: DedupScoreBreakdown | null | undefined
): PrimarySignal[] {
  if (!breakdown?.signals) return [];
  return breakdown.signals
    .filter((s) => isPrimaryKind(s.kind))
    .map((s) => ({ kind: s.kind, label: signalLabel(s.kind) }));
}
