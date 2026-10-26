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
- A strike counts once per file per run (`IntroTimeoutRunID`), so a retry round
  or a worker that stalls on everything cannot walk a healthy file to the limit
  in one run. A response in which at least two clips timed out and more than
  half of its clips did is read as a worker fault: those files are deferred
  with no strike. (The transport does not say which worker answered, so
  "two different workers" cannot be required.) The per-book op charges a
  timeout to the file the request actually carried, including the second file
  of a retry, and a silent book whose retry lands on a file that already holds a
  result is marked silent instead of being re-sent forever.
- A transcript of a file clears that file's strikes (and a `transcribe_timeout`
  verdict from them) when the bytes match. Strike writes go through
  `ModifyBookFile` (the per-file lock and compare-and-swap commit) and, in the
  per-book op, wait out a running library scan first. A book that already has a
  transcript is no longer relabelled `transcribe_timeout` because its first file
  holds the verdict, and a book that was never sent is no longer counted as an
  attempt.
- Long worker requests are supervised by polling the worker's `/health`
  (`busy_seconds`): an unreachable worker is an outage, a wedged one is "stuck",
  and the fixed 600 s request timeout is now a backstop derived from the worker's
  own limits. Stuck and outage are judged across every failing endpoint, not the
  last one, free jobs go to idle endpoints, and every error path returns the
  results that settled before it. Both ops apply those partial results and defer
  only the unanswered files; a book the transport dropped is deferred, not
  written as `whisper_error`. A worker older than `whisper_mlx_server.py` 1.8.0
  (no `busy_seconds`) is not polled and keeps the backstop alone.
- A worker child that cannot start or dies is reported as `worker_misconfigured`
  (deferred, nothing written) instead of a per-book `whisper_error`; a child
  that crashes on every clip therefore defers its files until the worker is
  fixed. `/health` no longer waits on the upload thread pool.
- Both ops report progress while a whisper request is outstanding, so a long
  but live request is not killed by the progress watchdog. The per-book op
  stamps liveness only (it no longer writes a progress number that could step
  the bar backwards), and the ticker stops even if the request panics.
- Operation logs now record an error attribute as its text; it was written as
  `{}` because an `error` value has no exported fields. A typed-nil error
  attribute is written as `<nil>` instead of panicking the logging call.
