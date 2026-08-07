<!-- file: changelog.d/20260807_114500_repair_transcribe_status.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7e5b12c8-4a09-4d63-b8f1-2c60943ae7d5 -->
<!-- last-edited: 2026-08-07 -->

### Fixed

- **~77% of the library was falsely marked `whisper_error`.** A day-long
  transcription-endpoint outage on 2026-07-01 tripped `processTranscribePage`'s
  whole-batch error path, which marks **every** book in a page as
  `whisper_error`. Measured 2026-08-07: 76.7% of a 300-book random sample carried
  the status, and **229 of those 230 books had good transcript text** — dated
  2026-06-27, four days before the outage. Across a 400-book sample the failures
  clustered into 17 timestamps, all on 2026-07-01, every error a connection
  failure. No transcript was ever damaged (`applyOutcome` correctly refuses to
  overwrite good text with nothing); only the status was wrong. The library had
  degraded into "everything looks broken while everything is fine" — the worst
  state for any query that filters on status, including the tiered backfill's
  "what still needs work" query.

### Added

- **`maintenance.repair-transcribe-status`** repairs rows whose stored error is a
  **transport** failure rather than a transcription failure: it recomputes the
  status from the stored text (credits → `ok`, else `unparsed`), or clears it back
  to never-attempted where there is no text. It never calls Whisper and never
  touches transcript text. Genuine failures (a model OOM, an ffmpeg codec error)
  and the `[SILENCE]` sentinel are deliberately left alone, and an *unrecognised*
  error is treated as genuine — wrongly clearing a real failure hides a broken
  file, while wrongly keeping one only costs a re-run. Defaults to `dry_run=true`.

  🔴 The principle it encodes: **an unreachable endpoint is "no attempt made",
  not "the attempt failed."** Recording it per-file blames the file for the
  network's problem. Same rule the intro classifier applies one layer up, where
  an absent transcript yields `unknown` rather than `prose`.
