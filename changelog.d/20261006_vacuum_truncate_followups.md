### Fixed

- **Post-vacuum WAL reset no longer reports "space still held" under steady
  activity writes.** After a converged PASSIVE, the post-vacuum TRUNCATE
  copied every frame but could not reset the `-wal` because a Record held
  the write lock at that instant (`busy=1`, `wal_frames == checkpointed`).
  With a 0 ms busy wait and one retry budget shared with the reader-held
  phase, `VacuumActivity` gave up with `ErrClampVacuumFailed`.
  `TestVacuumActivity_ReaderReleasedMidTruncateNeverTruncatesUnderTheCopy`
  failed in 23 of 450 loaded runs before the fix and 0 of 450 after, under
  the same concurrent load.
  - Busy TRUNCATEs now have their own retry budget
    (`vacuumTruncateBusyAttempts`, short fixed pause), separate from the
    not-ready attempts that back off while a reader holds PASSIVE short.
  - The post-vacuum TRUNCATE alone runs with a 25 ms busy_timeout
    (`vacuumTruncateResetWaitMS`) on a pinned checkpoint connection, which
    is restored to 0 afterwards (or the connection is discarded). Waiting for
    the write lock holds nothing Records need. The wait for readers while
    holding it is bounded at 25 ms, against Records' 10 s busy_timeout.
  - The give-up error and log now name the reason (`reader-held`,
    `checkpoint-busy`, `did-not-converge`, `busy-truncate`). Before, they
    always blamed a reader, even when the counts looked complete.
  - A database that is not in WAL mode (`0,-1,-1`) counts as done again
    instead of failing after about 1.1 s.
  - WAL restarts between PASSIVE rounds are detected from the `-wal` header
    generation, so a restarted WAL that grew past the previous checkpoint
    no longer counts as short.
  - The vacuum tests' fixtures must now leave more than
    `vacuumTruncateMaxLeftoverFrames` frames. A smaller fixture cannot tell
    the converging code from a TRUNCATE straight after the first PASSIVE.
- `TestVacuumActivity_LateReaderDoesNotStallWritersThroughTheTruncate` no
  longer fails with "no TRUNCATE was issued" (main CI, PR #3781). Its
  writers started on the first complete PASSIVE. Since #3784 that PASSIVE
  is followed by another round, so under load the writers committed past
  the late reader's snapshot first and no TRUNCATE was ever issued. They
  now start on the converged PASSIVE, after which the TRUNCATE is certain.
