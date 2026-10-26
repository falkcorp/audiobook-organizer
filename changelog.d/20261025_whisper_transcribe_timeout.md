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
- A strike counts once per file per run, and at most one strike per 12 hours
  (`IntroTimeoutAt`): both transcribe ops stamp the registry op id as the run
  id, so one stall seen by the per-book op and then by the per-file op is one
  strike, not two. A retry round or a worker that stalls on everything cannot
  walk a healthy file to the limit in one run. The worker-fault judgement is
  made per HTTP response in the transport, where the denominator is the
  worker's own batch: a response in which at least two clips timed out or
  crashed and more than half did (or in which any clip says the worker cannot
  decode) is a worker fault. Its verdicts are withheld, the worker is benched
  for the run, its jobs are re-queued on other workers, and the sound results
  that came with it are kept. A dispatch that failed anyway has its timeouts
  and crashes read as no answer (deferred, no strike) unless the dispatcher
  itself condemned the file by sending it alone to several workers. A lone job
  on a worker that is still answering `/health` is no longer blamed on the
  clip. The per-book op charges a timeout to the file the request actually
  carried, a retry-1 timeout is neither copied to the next file nor struck, and
  a silent book whose retry lands on a file that already holds a result takes
  that result (its real transcript, or the silence sentinel only when the held
  result is silence) instead of being re-sent forever.
- A transcript of a file clears that file's strikes (and a `transcribe_timeout`
  verdict from them) when the bytes match. Strike writes go through
  `ModifyBookFile` (the per-file lock) and, in the per-book op, are deferred
  while a library scan is running instead of making the page wait. A book that
  already has a transcript is no longer relabelled `transcribe_timeout` because
  its first file holds the verdict, and a book that was never sent is no longer
  counted as an attempt. A cancelled per-book run discards its page and writes
  nothing.
- Long worker requests are supervised by polling the worker's `/health`
  (`busy_seconds`): an unreachable worker is an outage, a wedged one is "stuck",
  and the fixed 600 s request timeout is now a backstop derived from the worker's
  own limits. Stuck and outage are judged across every failing endpoint, not the
  last one, free jobs go to idle endpoints, and every error path returns the
  results that settled before it. Both ops apply those partial results and defer
  only the unanswered files; a book the transport dropped is deferred, not
  written as `whisper_error`. A worker older than `whisper_mlx_server.py` 1.8.0
  (no `busy_seconds`) is not polled and keeps the backstop alone.
- A worker child that cannot start is reported as `worker_misconfigured`
  (deferred, nothing written, the worker benched for the run) on both
  transports, and `/health` reports it as `child_start_error` for five minutes
  (`WHISPER_CHILD_FAULT_TTL_S`) so the capability gate refuses that worker until
  it recovers. A child that dies while holding a clip is a new per-file
  `decoder_crashed` code, counted as a strike like a timeout so a clip that
  deterministically kills the decoder ends as a verdict instead of being re-sent
  forever. `/health` no longer waits on the upload thread pool and never answers
  503: a slow filesystem check yields 200 with the busy fields and unreadable
  roots, and a non-200 health response is not read as a probe.
- The client sends its request timeout as `X-Whisper-Request-Timeout-S` on both
  batch endpoints; a worker whose request outlives it stops after the clip in
  flight and answers the rest at once (`whisper_error: request abandoned`), so
  an abandoned request no longer keeps a worker busy for the length of the
  batch. A path batch that hits a start fault answers its remaining clips
  without waiting on extraction.
- Both ops report progress while a whisper request is outstanding, so a long
  but live request is not killed by the progress watchdog. The per-book op
  stamps liveness only (it no longer writes a progress number that could step
  the bar backwards), and the ticker stops even if the request panics.
- Operation logs now record an error attribute as its text; it was written as
  `{}` because an `error` value has no exported fields. A typed-nil error
  attribute is written as `<nil>` instead of panicking the logging call.
