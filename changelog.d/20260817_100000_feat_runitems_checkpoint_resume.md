### Added

- **`registry.RunItems` can now checkpoint and resume under concurrency.** Adds
  `ResumeFrom`, `CheckpointEvery` and `CheckpointStateFn` to `RunItemsOptions`.
  Concurrent checkpointing was previously refused outright — the comment said
  "parallel writes to reporter.Checkpoint would race on the shared state blob" —
  which left every op that follows the mandated worker-pool pattern unable to
  resume, and is why 100 of 140 op definitions were `ResumePolicy: drop`.

  The mechanism is a contiguous-completion watermark: a completion *count* is not
  a resume point when workers finish out of order (items 0,1,2,5 done is "4
  completed", and resuming at 4 would silently skip 3 and 4), so the checkpoint
  records only the unbroken completed prefix. A failed item never advances the
  watermark, so a resume retries it instead of skipping it.

  Behaviour is unchanged for callers that do not set the new fields.
