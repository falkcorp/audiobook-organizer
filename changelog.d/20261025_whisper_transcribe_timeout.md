### Fixed

- One audio clip that sends the Whisper decoder into an endless loop no longer
  wedges every worker. Each worker (`scripts/whisper_mlx_server.py`) now runs
  inference in a killable child process with a per-clip limit of
  `max(60 s, 4 x audio duration)` (`WHISPER_TRANSCRIBE_MIN_TIMEOUT_S`,
  `WHISPER_TRANSCRIBE_TIMEOUT_FACTOR`). On a timeout the child is killed and
  restarted and the file gets a per-file `transcribe_timeout` error. Upload
  handlers are plain `def`, so a busy worker keeps answering `/health`, which
  now reports `busy_seconds`.
- The transcribe dispatcher no longer reads its own 600 s timeout as "endpoints
  unreachable" and re-sends the whole batch to every worker. A worker that is
  reachable but past its limit is "worker stuck": the batch is split, the parts
  retried on other workers, results that completed are kept, and a file that
  leaves workers stuck on its own becomes a per-file `transcribe_timeout`.
  Both transcribe ops stop after three stuck batches in a row instead of
  deferring the library one batch at a time.
- `transcribe_timeout` is counted per book file (and per file hash, so a
  replaced file starts again). The second strike makes it a permanent per-file
  result that both `transcribe-book-intros` and `transcribe-file-intros` skip;
  `retry_failed` re-attempts it. A transcript that lands first is never
  overwritten. The aggregate stats gain a `transcribe_timeout` count.
- Both ops report progress while a whisper request is outstanding, so a long
  but live request is not killed by the progress watchdog.
- Operation logs now record an error attribute as its text; it was written as
  `{}` because an `error` value has no exported fields.
