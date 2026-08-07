<!-- file: todo.d/20260807_012000_transcribe_status_content_drift.md -->
<!-- version: 1.0.0 -->
<!-- guid: b471e5c9-2f68-4a03-95d1-0e37c8b2a6d4 -->
<!-- last-edited: 2026-08-07 -->

- [ ] **Investigate: 79% of books with a stored transcript are marked
  `whisper_error`.** Found incidentally while sampling a corpus for the
  three-outcome parser (2026-08-07), not chased — it is out of scope for the
  parser work but nobody will stumble on it otherwise.

  **The measurement.** A random offset-based sample of **987 distinct books that
  all have non-empty `intro_transcription`** breaks down by `transcribe_status`
  as:

  | status | count | share |
  |---|---|---|
  | `whisper_error` | 783 | **79.3%** |
  | `ok` | 177 | 17.9% |
  | `unparsed` | 26 | 2.6% |
  | `empty` | 1 | 0.1% |

  Every one of those 783 rows **has transcript text stored** while its status
  says the transcription failed. Status and content have drifted apart across
  what looks like most of the library.

  **Why it probably happens.** `applyOutcome`
  (`internal/plugins/maintenance/intro_transcribe.go`) writes
  `TranscribeStatus` on every outcome, but only writes `IntroTranscription` when
  the outcome carries a transcript. So a book transcribed successfully once and
  then re-attempted later — after the file moved, the GPU host went away, or the
  batch failed — keeps its old text and acquires a failure status. That is the
  same *shape* as the parse-vs-transcript divergence the parser PR guards
  against, one field over.

  **Why it matters.** Anything filtering on `transcribe_status == "ok"` is
  currently ignoring ~4 out of 5 books that actually have usable transcript text.
  Worth checking whether the tiered backfill's "needs work" query is one of them
  before it is sized — it would massively over-count the work remaining.

  **Do not assume it is a live failure.** The status could be a stale record of a
  historical outage rather than an ongoing one. Check
  `transcribe_attempted_at` vs `intro_transcribed_at` on the affected rows first:
  if attempted is consistently much later than transcribed, this is drift from
  old re-runs, not a currently-failing pipeline. 🔴 The distinction changes the
  fix completely, so measure before concluding.

  Related: [[per-file-intro-identity-signal]].
