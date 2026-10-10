- [ ] **SCORE-MISSING-AUTHOR-FETCH** 02-PR9a (#3900) ranks the Search and Browse
      dialogs by `rank_score` (no penalty for a missing author or narrator), but
      `score` still carries the 0.75 / 0.85 penalties everywhere else, including
      `pickBestMatchFromScored` (`internal/metafetch/service_scoring.go`, the
      `score *= 0.75` no-author and `score *= 0.85` no-narrator branches), which
      feeds `bestTitleMatchForBook` on the single-book fetch path in
      `internal/metafetch/service_fetch.go` (`FetchMetadataForBook` at :274,
      `FetchMetadataForBookByTitle` at :474). That path does NOT go through
      `applygate`: both take `scored[0]` and apply it via `guardedApply` ->
      `CommitApply`, which checks only transcribed-title agreement, the
      review-only source rule, `ApplySeriesPositionFilter` and the fill-only
      `StripFilledFields`. Unattended callers: `audiobooks/organize.go:54`,
      `organizer/service.go:373`, `itunes/service/importer.go:1086`,
      `scheduler/extra_ops.go:918`, `server/entities_ops.go:269`; manual:
      `handlers/metadata/book_scan_lock.go:404`. Making that pick neutral needs
      its own gate on those applies first (a score floor and the sequence guard),
      otherwise a title-only author-less candidate can win and be written. Also
      `docs/architecture/identification-pipeline.md` "Stage 4" documents the
      factors; say there that `rank_score` leaves them out.
