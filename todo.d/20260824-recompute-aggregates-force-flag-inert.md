## 🔴 `recompute-book-aggregates`'s `Force` flag is inert, and two log lines advertise it

Found 2026-08-24 while auditing the aggregate-recompute safety net for PR #2861. Not
fixed there — different file, different call path.

`internal/maintenance/jobs/recompute_book_aggregates.go` short-circuits on the one-time
sentinel:

```go
// Check the one-time backfill sentinel. If already done and Force is false,
// report the count of books that would be processed and return early.
if !dryRun && pebbleStore.IsBookAggregatesBackfillDone() {
    slog.Info("... skipping. Use Force=true to override.")
    reporter.Log("info", "Backfill already completed — skipped. Use Force=true to override.", nil)
    return nil
}
```

**`Force` is not in that condition, and is never read anywhere in `Run`.** It is declared
once, in `DefaultParams` (`:51`), and that is the only mention outside comments and the
two operator-facing strings above. The parameter cannot even arrive: the sole call site,
`internal/server/maintenance_job_op.go:187`, passes `p.DryRun` from
`maintenanceJobOpParams`, which has exactly one field — `DryRun bool`. A submitted
`{"force": true}` is discarded before it reaches the job.

Net effect: **once the sentinel is set, this job can never run again**, and the escape
hatch it prints to the operator does nothing. The comment at `:75` describes a guard
condition the code does not implement.

### Why it matters more than it looks

`notifyBookFileChange` (`internal/database/pebble_store_book_aggregates.go:180-189`)
justifies swallowing recompute errors partly on the grounds that "the backfill job acts
as a safety net for any misses." That net is inert once the sentinel is set. A batch write
whose recompute fails for N books logs N warnings, reports success, and the documented
remedy refuses to run.

Timing note, so this is not overstated: before the 2026-08-19
`resolveAggregatesBackfillMarker` fix, prod fell through to `runViaInterface`, which never
writes the sentinel — so the net was accidentally live. It is now one clean non-dry run
away from permanent disablement. **Whether prod's sentinel is currently set was NOT
verified.**

### Fix

Either wire `Force` through (`maintenanceJobOpParams` needs the field, `Job.Run` needs to
carry it, and the sentinel check needs `&& !force`), or delete the parameter and both log
lines that promise it. Do not leave a third state where the flag exists and lies.

- [ ] Decide: wire `Force` through, or remove it and correct the two operator messages
- [ ] Check prod: is `system:backfill:book_aggregates_v1_done` currently set?
- [ ] Re-check `notifyBookFileChange`'s "backfill job acts as a safety net" clause once resolved
