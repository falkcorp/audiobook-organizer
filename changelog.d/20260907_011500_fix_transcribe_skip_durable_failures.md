### Fixed

- **The "Transcribe book intros" job no longer re-attempts the same broken books
  on every run.** Selection decided what to transcribe purely from whether a book
  had a stored transcript, and books that failed for a durable reason — the source
  audio file is gone, the book has no audio file, or ffmpeg could not decode the
  bytes — never get a transcript, so they were re-selected and re-processed every
  single run. On the 2026-09-05 run that was 9,151 of 9,922 work items (92%): the
  run spent almost all its effort on books it had already established it could not
  transcribe, and only 14 succeeded. Selection now consults the recorded
  `TranscribeStatus` and skips those durable-failure books by default, while
  **auto-retrying them the moment their source file is back on disk** (or, for an
  ffmpeg failure, has been replaced) — so once the missing-file repoint restores a
  book's audio, the next transcribe run picks it up on its own with no flag. The
  run log now reports how many books were skipped as known-broken, so they don't
  silently vanish from the count. A new `retry_failed=true` parameter (mirroring
  `retry_silence`) forces every durable-failure book back into the run.
