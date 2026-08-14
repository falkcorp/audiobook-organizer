## F110 measured: playlist-item PID coverage is 88.5% via ExternalIDMapping — F111 is GO

Measured on production 2026-08-14. Two instruments, very different answers —
the brief named the wrong one:

- **The RIGHT instrument — `ExternalIDMapping` (track-PID → book), i.e.
  `GetBookByExternalID("itunes", pid)`:** this morning's post-#2367 boot
  backfill completed `tracks_processed=97981 registered=86732` — **88.5% of
  all XML tracks** now resolve to a book at track level. This is the lookup
  F111's importer must use.
- **The instrument the F110 brief named — `GetBookByITunesPersistentID`
  (album-level `Book.ITunesPersistentID`):** only 13,128 books carry the
  field (API `/api/v1/itunes/books` page-through = boot log exactly), and
  only 14.0% of the 84,296 distinct user-playlist PIDs resolve through it.
  **Do not use it for playlist import** — it answers a different question.
- Current XML (`iTunes Library.xml`, 160MB, 2026-07-19): 269 user playlists
  carry materialized `Playlist Items`, 98,184 refs. Under even the weak
  album-PID instrument the distribution is bimodal: **124 playlists at 100%
  coverage**, 96 at 1–49%, 13 at 0% — the mapping instrument strictly
  improves on this.

**Verdict: coverage does NOT moot the recovery paths (the F110 gate
question). F111 (static-snapshot import by PID) is GO** once the owner
green-lights the run — resolve via `GetBookByExternalID("itunes", pid)`,
import as idempotent one-shot maintenance op, verify by re-reading the DB.
Exact per-playlist import counts come from that run's report.
