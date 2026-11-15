- [ ] **TODO-OUTBOX-ROUND-CANCEL** Settle the completed part of a canceled
      unordered round in `core/outbox` — `passUnordered` drops the whole round
      when the run's context ends (`deliver.go`, "nothing is settled: the round
      is delivered again"), so every handler that already returned nil in it is
      delivered again by the next relay run. A round holds up to
      `4 x unordered_parallelism` messages (window), so a graceful shutdown
      redelivers up to 16 messages that took effect at the default
      parallelism of 4, on every restart. The ordered path (`settleAck`,
      `settleFail`) already commits past a cancel on `settleCtx`; the round
      does not, and its comment documents redelivery for a crash only.
      Requirements for the fix: settle the done items in one batch on
      `settleCtx`, but `settleRound` moves the cursor to `newMax`
      unconditionally and skips items that are not done, so on a partial round
      it would skip the unfinished new messages for good. `newMax` must be
      capped at the highest contiguous done sequence, or each unfinished item
      must go into the try record due now with its attempt unchanged. A
      handler error that arrives after the cancel must count as not done (it
      may be the cancel's own error), or it would burn an attempt or
      dead-letter. A skipped message is worse than a duplicate, so the test
      must cancel mid-round with some items done and some not and assert that
      the done ones are acked, the rest are delivered once by the next run,
      and none is skipped.
