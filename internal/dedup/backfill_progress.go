// file: internal/dedup/backfill_progress.go
// version: 1.2.0
// guid: f6a7b8c9-d0e1-f2a3-b4c5-d6e7f8a9b0c1
// last-edited: 2026-07-08

package dedup

// BackfillVersionMarker identifies the current generation of the dedup
// backfill pipeline. Bumping this string causes a one-time re-run on the
// next startup so older deployments can pick up new rules. Rule history:
//   - v1: initial backfill (PR #203)
//   - v2: non-primary-version filtering, same-group pair skipping, and
//     Layer 1 exact checks during FullScan (PR #207)
//   - v3: skip books with empty/near-empty titles, skip Layer 1 + Layer 2
//     matches where books are distinct volumes of a numbered series
//     (PR #208, first iteration)
//   - v4: expanded series-marker regex to include "bk", "vol", "volume",
//     "number", "no", "part", "pt", "episode", "ep", "#", and added a
//     last-ditch digit-only-difference fallback to catch series volumes
//     whose marker the regex doesn't recognize
//   - v5: previous version
//   - v6: re-run to pick up authors stranded on a stale embedding model.
//     The Jul 2 2026 cutover from OpenAI text-embedding-3-large (3072-dim)
//     to local bge-m3 (1024-dim) reconciled books via the dedicated
//     dedup.reembed-embeddings op, but that op is books-only — author
//     vectors were never re-embedded. Every restart since then,
//     HydrateChromem tried to mirror ~3,450 authors' stale 3072-dim
//     vectors into the 1024-dim ANN store and logged a dimension-mismatch
//     warning per author (harmless spam, but those authors also had zero
//     Layer 2 embedding-dedup coverage). runEmbeddingBackfill's author
//     loop (embedAuthorsConcurrent -> EmbedAuthor) is already model-aware
//     (PR #1744) and would have fixed this on its own, but it only runs
//     once per marker generation — v5 predates the cutover, so it never
//     fired again. Bumping to v6 triggers one more pass; books cache-hit
//     immediately (already reconciled), authors get freshly embedded.
const BackfillVersionMarker = "embedding_backfill_v6_done"

// NewDedupScanProgressLogger returns a progress callback suitable for
// Engine.FullScan that logs once every `interval` books processed (plus
// one final line at completion), per phase.
//
// It exists because FullScan passes `done = i+1` at a step of 10, so values
// are 1, 11, 21, ... which never satisfy `done % interval == 0` for interval
// ≥ 11. This previously hid all scan progress. The returned closure tracks
// the next threshold internally and advances past it on each bucket crossing,
// so progress lines appear at ~interval granularity regardless of the caller's
// step size.
//
// FullScan now reports two phases — "scan" (Layer 1/2 exact + embedding
// checks) and "score" (unified composite scoring) — each of which iterates
// over all books independently. The bucket-crossing state (nextLog) is reset
// whenever the phase changes so the second phase doesn't inherit the first
// phase's already-advanced threshold and go silent for its own first
// `interval` books.
func NewDedupScanProgressLogger(interval int, logf func(format string, args ...any)) func(phase string, done, total int) {
	if interval <= 0 {
		interval = 1
	}
	nextLog := interval
	lastPhase := ""
	return func(phase string, done, total int) {
		if phase != lastPhase {
			if lastPhase != "" {
				logf("[INFO] Dedup scan phase %q complete, starting phase %q", lastPhase, phase)
			}
			lastPhase = phase
			nextLog = interval
		}
		if done >= nextLog || (total > 0 && done == total) {
			logf("[INFO] Dedup scan progress (%s): %d/%d", phase, done, total)
			for nextLog <= done {
				nextLog += interval
			}
		}
	}
}
