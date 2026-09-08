- [ ] **Give the Pebble→SQLite activity migration a visible status.** The
  checkpoint half is done (see below); the status half is not. Today the only way
  to learn that the migration is running, how far along it is, or why
  `read_secondary` is still false, is to grep raw log text through activity
  search. There is no operation, no progress, no surfaced reason. The user asked
  for it to "always have some sort of status like all the other things in
  activities".

  Two routes, to decide with the shutdown numbers below on the table:

  - **As a registry op** — best UX (`ActiveDefs()` has no allowlist, so a
    registered op shows up immediately, and `Reporter` gives progress + logs).
    **Requires closing a 2s gap first.** `Registry.Shutdown` drains bounded by ctx
    (10s, `server_lifecycle.go:405`), then joins `goroutineWG` behind a
    **hardcoded 2s escape** (`registry.go:1188`) and returns regardless.
    `worker.go:359` claims that final join "genuinely covers plugin code" — true
    only if the goroutine exits within those 2s. `streamTierEntries` checks ctx
    every 64 rows (~0.26s at production's 244 rows/s), but once inside `fn` the
    two `recordBatch` passes over a 500-row batch run to completion — **~2.05s**,
    i.e. right at the boundary. Past it the caller closes Pebble under a live
    reader, where Pebble *panics* rather than errors. Fix: check ctx between the
    copy and parity passes inside `fn`, halving worst-case exit to ~1s. Today
    `sqlMigrationStarter.Stop` sidesteps all of this with an unbounded join.
  - **As a read-only status endpoint** over the checkpoint blob — no shutdown risk
    at all, but not "where the other operations are", which is what was asked.

  **Denominator is open either way.** Progress needs a per-tier total. Counting
  all 7 tiers up front is a full keyspace walk; prefer counting lazily at each
  tier's start, persisting the count in the checkpoint blob so resumes don't
  recount, and rendering it approximately — dual-writes grow a tier while it is
  being scanned, the same reason `maintenance.activity-reclaim` renders `≈N`.

- [x] **Checkpoint the migration so a restart doesn't start over.** DONE: adds
  `ActivitySQLBackfillProgressKey`, a per-tier `{state, cursor, scanned, copied,
  reinserted}` blob written every batch (NoSync) with verdict transitions synced,
  and threads a `startAfter` lower bound through `streamTierEntries`. Production
  restarted the migration from row zero three times on 2026-09-08, discarding
  ~81 minutes, then spent ~3h re-reading 6.5M rows it had already copied.
  **The non-obvious half:** a bare cursor would have been a data-integrity bug.
  `ParityOK` was in-memory only and reset to `true` at the top of every run, so it
  was fail-closed only *by accident* — a crash forced a full re-verify. With a
  cursor, a run that failed parity, died, and resumed would re-derive "clean" from
  its untainted tail and flip reads onto an unverified copy. The verdict is now
  durable per tier, `reinserted` carries across resumes, a `failed` tier is
  re-scanned in FULL before it can pass, and the sentinel gate is a census
  (`AllTiersClean`) rather than a process-local flag.
