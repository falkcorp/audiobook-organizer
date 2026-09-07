- [ ] **`metadata.batch-apply-cached` still has no resume — and a bare
      `ResumePolicy` flip would make it worse, not better.** Deferred from the
      apply-path collision-resolver PR (`fix/apply-path-collision-resolver`),
      which deliberately shipped only the collision resolver and the durable
      per-item failure record. The op never calls `reporter.Checkpoint`, so
      switching it from `ResumeDrop` to `ResumeRestart` on its own would restore
      `state_bytes=0`, hand back unmodified params, and **re-run every book_id
      from the top on each restart** — an unbounded re-apply loop, strictly worse
      than today's drop. Four things have to land together:
      - a checkpoint field on `batchApplyOpParams` plus a periodic
        `reporter.Checkpoint` (`internal/server/batch_apply_op.go`);
      - **a done-set or contiguous watermark, NOT a `LastBookID` cursor.**
        `intro_transcribe.go:346` can use a cursor because it is sequential;
        this loop is parallel at `writeBackWorkers()`
        (`batch_apply_op.go:164-166`), so books finish out of order and a
        last-ID cursor silently skips the gaps;
      - a nonzero `MinCheckpointInterval`, or the watchdog's
        uncheckpointed-strike path never engages (`watchdog.go:186`);
      - reconciliation with `mergeBatchApplyQueuedParams`
        (`batch_apply_op.go:108`) — `registry/types.go:76` records a real
        incident where *"metadata.batch-apply-cached ran discarded the new
        book_ids"*, so resume plus params-merge is a known sharp edge here.
