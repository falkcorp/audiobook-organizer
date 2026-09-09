### Added

- **A queued operation now says how many books it covers, before it starts.** A
  pending "Apply Cached Metadata" run read only `Waiting to start…`, giving no
  way to tell a batch of one book from a batch of twelve hundred — and the
  number was not even fixed: approving more books while a run waits merges them
  into that same pending row, so it grew silently while showing nothing. The
  Activity page and the notification bell now show its current size (`1204 books
  to apply`), restated the moment the queue merger grows it. The same applies to
  pending "Batch Save to Files" and "Bulk Tag Write-back" runs, which had the
  identical blind spot.

  Ops report this through a new optional `OperationDef.SummarizeQueued` hook,
  which the registry invokes at every moment a queued row's work can change and
  nowhere else: each of the three paths that creates a queued row (a normal
  enqueue, the requeue that replaces an interrupted run with a fresh one, and
  the batch flush that folds a bucket of subjects into one op), the merge that
  unions newly requested work into a row already waiting, and the restart that
  re-queues an interrupted run against its remaining work. That last one
  matters most for metadata apply, whose restart policy sends it back through
  the queue on every resume. Ops that declare no hook behave exactly as before.
