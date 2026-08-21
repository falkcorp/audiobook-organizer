// file: web/src/components/review/evidence/signalLabels.ts
// version: 1.0.0
// guid: 6d8b3f51-2a94-4c07-ae63-5f10c7d92b48
// last-edited: 2026-08-20
//
// Reviewer-facing names for dedup signal kinds, so the dupes rows can say WHY a
// pair is on screen without the reviewer expanding anything.
//
// This exists because of what the triage set actually looks like: no CERTAIN
// pair rests on a fuzzy title match -- every one is backed by exact_file,
// isbn_asin or metadata_hash -- so the reviewer is never adjudicating a
// resemblance. They are confirming a named signal. That is a one-line read, and
// making them open a panel for it is the round-trip worth removing.

import type { DedupScoreBreakdown } from '../../../services/api';

/**
 * Kind -> reviewer-facing label.
 *
 * Deliberately close to the Go constants in internal/dedup/unified/score.go
 * rather than freely reworded: these are the names that appear in the evidence
 * panel, the config's per-kind overrides and the logs, and a chip that calls a
 * signal something no other surface calls it costs more than the raw string
 * would. Unknown kinds fall through to the raw kind (see `signalLabel`), so a
 * new collector renders its own name instead of nothing.
 */
const SIGNAL_LABELS: Record<string, string> = {
  exact_file: 'exact file',
  exact_acoustid: 'exact audio',
  isbn_asin: 'ISBN/ASIN',
  lsh_acoustid: 'audio fingerprint',
  embedding_high: 'embedding (high)',
  metadata_hash: 'same source record',
  metadata_fuzzy: 'fuzzy title/author',
  embedding_med: 'embedding (medium)',
  duration: 'duration',
  folder_path: 'folder path',
};

export function signalLabel(kind: string): string {
  return SIGNAL_LABELS[kind] ?? kind;
}

export interface PrimarySignal {
  kind: string;
  label: string;
}

/**
 * The primary signals behind a candidate, in the order the scorer recorded
 * them. Empty when there is no breakdown or none is marked primary.
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
    .filter((s) => s.primary)
    .map((s) => ({ kind: s.kind, label: signalLabel(s.kind) }));
}
