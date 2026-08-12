- [ ] **`wipeActivity` dry-run count saturates at 2.** `wipeActivity` in
  `internal/server/maintenance_fixups.go` reports its dry-run row count from
  `svc.Query(ctx, ActivityFilter{Limit: 1})`'s `total`. Since the bounded-scan
  change in `0adf6e97`, `total` is a LOWER BOUND: the walk stops once it has
  collected `Offset+Limit+1 == 2` matches, so the dry-run preview now reports
  "2" no matter how many activity rows actually exist. The wipe itself is
  unaffected (it calls `WipeAllActivity`), so this is a misleading preview
  rather than data loss — but the preview is exactly what an operator uses to
  decide whether to run the wipe. Fix needs either a dedicated count path or a
  `CountByPrefix`-style call rather than reusing the paged query. Noted inline
  at the call site during the activity-cancellation work (branch
  `fix/activityquery`).

- [ ] **`WipeAllActivity` still does an uncancellable full scan on a request
  path.** It calls `scanTierKVs(context.Background(), ...)` per tier, and is
  reachable from `handleWipe`. The activity-cancellation work deliberately left
  the maintenance methods (`Prune`, `WipeAllActivity`, `Summarize`,
  `CompactByDay`) context-free per scope, so `Query`/`GetDistinctSources` are
  cancellable but this path is not: an abandoned wipe request still scans every
  tier to completion. Lower severity than the query path (a wipe is rare and
  operator-initiated, not fired on every page load) but it is the same shape of
  defect and the same fix.
