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
  for the run, and the sound results that came with it are kept. A worker that
  faulted, for either reason, gets no more work for the rest of the dispatch
  while an endpoint that has not faulted remains, and every strike it answered
  in that dispatch (one from an earlier chunk of the faulted request included)
  is withheld and its job re-sent to the endpoints that have not faulted. Only
  once every endpoint has faulted are the faulted ones asked again and their
  answers believed: the jobs of a fault made of timeouts go back in requests of
  at most two clips, so each clip reaches its own verdict even when every
  worker faulted on the same page of looping clips, and a worker that cannot
  decode at all (`worker_misconfigured`) is never sent back the jobs it refused.
  A dispatch that failed anyway has its timeouts
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
- Both ops report progress while a whisper request is outstanding, the
  per-book op's silence retry rounds included, so a long but live request is
  not killed by the progress watchdog. The per-book op
  stamps liveness only (it no longer writes a progress number that could step
  the bar backwards), and the ticker stops even if the request panics.
- Operation logs now record an error attribute as its text; it was written as
  `{}` because an `error` value has no exported fields. A typed-nil error
  attribute is written as `<nil>` instead of panicking the logging call.
- A worker-fault response no longer re-queues its jobs without bound. Each job
  remembers the workers that faulted on it; after two (or every worker, in a
  smaller pool) it is deferred with the dispatcher's `worker_fault` code, never
  struck. A fault from a one-worker pool defers the jobs instead of stopping
  the run, and a worker that faulted in a dispatch is not asked again in it
  while a worker that has not faulted remains. When every endpoint is benched, or every
  worker's decoder is restarting, the dispatcher waits and re-checks every 30 s
  for up to 6 minutes before reporting the pool unavailable
  (`ErrPoolUnavailable`); the per-book op stops after five unavailable pages in
  a row (`errWhisperUnavailable`), and any other page (answered, stuck, or
  failed some third way) starts the count again. Under `require_gpu` an
  endpoint whose `/health` cannot be read is waited for the same way, so a pool
  whose tunnels are all down ends as `ErrPoolUnavailable`: the per-book op
  counts it toward the five-page stop and the per-file op retries the batch and
  then stops the run, instead of both deferring every page in under a second.
- A second `transcribe_timeout` strike only counts when a different machine
  gave it (`IntroTimeoutWorker`, the machines that struck the file). One sick
  Mac can no longer make a clip's timeout permanent by itself. A worker is
  identified by the `machine_id` its `/health` reports (`whisper_mlx_server.py`
  1.12.0: a hash of `WHISPER_MACHINE_ID`, `/etc/machine-id`, the macOS platform
  UUID, or the hostname, computed once on a background thread at start-up so
  `/health` never runs `ioreg` on the event loop), stored as `m:<hash>`, a known
  machine. A worker that does not report one (an MLX worker older than 1.12.0,
  or the faster-whisper `whisper_server.py`, which does not report it) is
  identified by a hash of its URL, `u:<hash>`: an unknown machine, since two
  such ports may be one box. An endpoint whose `/health` could not be read
  names no machine. A strike from an unknown or unnamed machine can only be a
  file's first: a later strike counts only when it names a known machine that
  is not among the known machines already stored, so two ports on one Mac count
  as one machine once the workers report `machine_id`, and before that their
  second strike does not count. The first known machine to strike a file
  struck only by unknown ones is recorded, not counted, because it may be one of
  them under its new identity. A condemnation by the dispatcher needs a lone
  clip stuck on two different machines; it counts as two machines, and as a
  strike even when the same pair condemned the file before, only when it names
  two distinct known machines. What is stored, and returned in API responses,
  is that opaque identity, never an endpoint address; a raw endpoint URL
  stored by a pre-release build is dropped on the file's next strike write. A
  file that carries a strike is sent to a healthy endpoint on another machine
  when there is one, even at a lower priority, and to the same machine when
  there is not; that holds both for ordinary allocation and for the small
  requests the dispatcher re-sends after a split or a worker fault.
  Trade-off: on any pool whose routing sends the file to the same machine each
  time (a pool of one machine, or a night on which every other machine is
  down), and on any pool in which no worker reports `machine_id` (faster-whisper
  workers only), a timeout does not become permanent; the clip stays at its
  strikes and is re-sent each run. Rollout: a worker that starts reporting
  `machine_id` changes identity once; that first known identity is recorded
  without a strike, so the upgrade cannot give a file a second strike from the
  machine that gave the first. A file struck before the upgrade then needs
  strikes from two different known machines to become permanent.
- A one-file book whose 300 s clip timed out, or was condemned by the
  dispatcher (which is what a worker older than the per-clip deadline
  produces), is deferred rather than sealed as `[SILENCE]`: nothing listened
  past 90 s. A multi-file book whose 300 s clip got no verdict is not sealed
  either, at either retry stage; words from the second file are still its
  answer. Cost: such a book (typically a long music intro) is re-sent on every
  nightly run until its 300 s clip gets a verdict.
- New worker codes: `decoder_killed` (the decoder child died from SIGKILL or
  SIGTERM, or was never reaped) and `request_abandoned` (the request outlived
  the client's deadline). `request_abandoned` is never a verdict (deferred, no
  strike, no status). `decoder_killed` is a strike like a timeout: one kill
  could be the machine's (macOS jetsam, an operator), so it turns permanent
  only when a second machine also fails on the clip, and a decode that grows
  until it is killed settles instead of being re-sent forever. A child that
  crashes by itself is still `decoder_crashed`. The deadline header is rounded up to whole
  seconds, so the worker never gives up before the client.
- A first-pass timeout on a first file the per-file op already heard as silence
  goes on to the 300 s retry instead of deferring the book on every run.
- `clear_run_id` with a per-book run id also rolls back the book-level
  `transcribe_timeout` that run stamped when it made a file permanent (only
  while the book still holds exactly that verdict and no transcript). A book
  stamped from its first file's permanent verdict without being sent is due
  again once that file's verdict is gone. The clear summary now counts "book
  results (transcripts or timeout verdicts)".
- A liveness probe that cancels a request in the instant after the request
  already failed (an HTTP 500, say) no longer relabels that failure as a stuck
  or unreachable worker: the cause is read only when the request's error wraps
  that cause or is `context.Canceled`.

#### Deployment order

Deploy the Go server **before** updating the Mac workers. A server older than
this change does not know the new worker codes (`transcribe_timeout`,
`decoder_crashed`, `decoder_killed`, `request_abandoned`) and stores each as a
per-file and per-book `whisper_error`: not durable, but wrong. The new server
reads `request_abandoned` as "no answer" and counts the other three as
strikes. Old workers keep working against the
new server: they never send the new codes, and the dispatcher condemns a clip
that hangs one (deferred at retry 1, see above).

#### Corrections, made before release

- The worker-fault entry above said a faulted worker's jobs are re-queued on
  other workers; they are re-queued at most until two workers (or every worker)
  have faulted on them, then deferred.
- The strike entry said the second strike makes a timeout permanent; it must
  also come from a different worker.
- The `decoder_crashed` entry said a clip that deterministically kills the
  decoder ends as a verdict; it does once it has crashed two different
  machines. On a one-machine pool it stays at one strike and is re-sent each
  run, like a timeout.
- The different-worker entry said a "worker" was an endpoint and that only a
  single-worker setup never makes a timeout permanent. A worker is a machine
  (two ports on one box are one), and the price is paid by any pool whose
  routing sends the file to the same machine; a struck file is now routed to
  another machine when one is healthy.
- The worker-fault entry said a faulted worker's jobs are kept off it; that now
  holds only for `worker_misconfigured`. A fault made of timeouts re-queues its
  jobs in requests of at most two clips that any worker may take.
- `decoder_killed` was listed as a worker code that is never a verdict; it is a
  strike that only a second machine can make permanent (owner decision).
- An unreachable `require_gpu` pool was not waited for: every endpoint was
  refused at once as a capability problem. It is now paced and reported as
  `ErrPoolUnavailable`.
- The liveness entry said the cause is read only when the request's own error
  is `context.Canceled`; it is also read when the error wraps the cause.
- The unavailable-page streak was not strictly "in a row": a stuck page did not
  reset it. Any page that is not unavailable now does.
- The progress entry did not cover the per-book op's silence retry rounds,
  which ran without the liveness tick.
- The worker-fault entry said only a `worker_misconfigured` worker had its jobs
  kept off it and that the jobs of a fault made of timeouts could go to any
  worker. The worker that timed out took them too, and its per-clip timeouts on
  them were believed, so a sick worker gave first strikes to healthy files (8
  of 32 on two workers in review). A worker that faulted for any reason now gets
  no more work in that dispatch while one that has not faulted remains, and its
  strikes from that dispatch are withheld.
- The different-machine entry said two server ports on one Mac never count as
  two machines. That held only once the workers report `machine_id`: two old
  ports were two machines, a worker's upgrade gave it a new identity that could
  strike the same file again, and a condemnation by two old ports counted as
  two machines. Identities that are not `m:` now never give a counted later
  strike, the first known one after them is recorded only, and a condemnation
  counts as two machines only with two distinct known ones.
- The routing entry covered only ordinary allocation. The requests re-sent
  after a split or a worker fault took the first endpoint by priority, the
  machine that had already struck the file included.
