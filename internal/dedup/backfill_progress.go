// file: internal/dedup/backfill_progress.go
// version: 1.1.0
// guid: f6a7b8c9-d0e1-f2a3-b4c5-d6e7f8a9b0c1
// last-edited: 2026-07-04

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
//   - v5: current version
const BackfillVersionMarker = "embedding_backfill_v5_done"

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
