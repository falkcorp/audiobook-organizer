// file: web/src/components/audiobooks/rankScore.ts
// version: 1.0.0
// guid: 0d7ffc63-1c5c-4720-b665-1499cfd6fc4a
// last-edited: 2026-10-10

/**
 * The score the Search and Browse dialogs rank a result list by: the backend's
 * rank_score (the score without the penalties for an author or narrator the
 * result does not name), or score for a row that predates it. Display only:
 * the apply gates and the stored candidate order read `score`.
 */
export function rankScoreOf(c: { score: number; rank_score?: number }): number {
  return c.rank_score ? c.rank_score : c.score;
}
