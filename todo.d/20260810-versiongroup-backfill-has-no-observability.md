- [ ] **VGBACKFILL-OBS** `BackfillVersionGroupIndex` cannot be observed, so a
      deployment cannot confirm the prod repair it exists to perform. **Found
      2026-08-10 while verifying the #2288 deploy on prod.**

      The function (`internal/database/pebble_store_versiongroup_backfill.go:38`)
      logs **exactly one line, only on success** —
      `versiongroup-backfill scanned indexed`. Every other outcome is silent:

      - **Sentinel already set** → `return nil` at `:39-42` with no log.
      - **Type assertion misses** → `internal/server/server_lifecycle.go:1018-1023`
        does `s.Store().(vgBackfiller)`; if the concrete store is ever wrapped or
        decorated, the `ok` branch is skipped and *nothing at all* happens. No
        log, no error, no metric.
      - **Still running** → no start log, so a long scan is indistinguishable
        from having never started.

      **Measured on prod after deploying `76269d57` at 21:23 EDT:** 12+ minutes
      later there was no `versiongroup` line in the journal at any priority, no
      warning, and no error. memdb warmup had completed at 21:25:21 (366,922
      books). The process was busy at 256% CPU — but that is attributable to
      `acoustid.backfill`, which started at 21:23:43 over 44,877 books. **Which
      of the three states above the backfill was in could not be determined from
      outside.** That is the finding; nothing here claims the backfill failed.

      This is the same failure class as the seven "reporting success while
      meaning nothing" mechanisms already recorded: a silent skip is
      indistinguishable from a silent success. The v1→v2 sentinel bump in #2288
      was specifically designed so that deploying performs the prod repair — and
      there is currently no way to confirm it did.

      **Fix:** log at every exit — `starting` (with the sentinel key and the
      book-scan bound), `skipped, sentinel present`, and the existing completion
      line; and log at WARN when the type assertion fails rather than falling
      through silently. A one-time repair whose execution cannot be verified
      should be treated as not having run.

      **Then re-verify on prod**, since the current state is genuinely unknown.
