- [ ] **Verify op-ID audit trail on prod after deploying the run-context fix.** Trigger
  one low-risk maintenance op (`maintenance.temp-file-cleanup` is the safest — it
  records changes but only touches orphaned `*.tmp.m4b`/`*.tmp.m4a` files) and confirm
  `operation_changes` now has rows keyed to that op ID. Until this is observed on prod,
  the fix is verified only by unit test. The prod check is the one that matters: the
  wiring passes through `wireServerFromContainer`, which no test exercises.
- [ ] **Historical gap is permanent — do not chase it.** Every maintenance op run before
  this deploy recorded no `operation_changes` rows. Those runs cannot be reverted and the
  history cannot be reconstructed; the data to rebuild it was never written. Relevant when
  investigating anything that happened before 2026-08-14: an empty change list for a
  pre-fix run means "recording was off", not "nothing changed".
- [ ] **Audit the eight `ctxOpID` consumers now that the ID actually arrives.** Their
  `CreateOperationChange` calls have never executed in production, so their payloads have
  never been exercised against real data — a wrong field or a panic in one of those
  branches would have been invisible until now. Worth one read-through of each call site
  (`series.go`, `cleanup.go` x2, `write_back.go`, `reconcile.go`, `dedup_ops.go`,
  `optimize.go`, `metadata.go`) before or shortly after the deploy.
