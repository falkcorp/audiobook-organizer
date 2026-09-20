- [ ] **`metadata.batch-apply-cached` re-picks books whose refusal can never change.**
      Measured 2026-09-20 over 4 completed batches: 359 gate refusals across only
      196 distinct books — 163 of them refused TWICE within ~40 minutes. The
      refusals are structural (a duplicated file set, a path with no author in
      it, a candidate that will never match a transcribed title), so nothing
      about them differs on the next run. The backlog cannot drain; every future
      batch re-pays the same cost and the owner sees another `applied 0 of N`.
      Needs a suppression/backoff so a book refused for a stable reason is not
      re-offered until the inputs that caused the refusal change.
      Evidence: per-book `book not applied` lines in the op logs (`GET /operations/v2/<id>/logs/download`, zstd).
