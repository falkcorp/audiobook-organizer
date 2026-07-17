<!-- file: docs/audits/2026-07-17-multi-discipline-review.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3e8291d4-6ac0-47e7-82c1-588438d96070 -->
<!-- last-edited: 2026-07-17 -->

# 2026-07-17 Multi-Discipline Review — Findings

Four parallel discipline reviews run against main @ a3cef740 on 2026-07-17:

| Discipline | Findings | Breakdown |
|---|---|---|
| Dedup / identification subsystem | 7 | 2 HIGH · 3 MEDIUM · 2 LOW (F1 since fixed by #1973) |
| Pipeline / operations layer | 23 | 5 data-loss · 7 correctness · 9 reliability · 2 perf |
| Logging coverage | 24 | 7 critical · 9 high · 8 medium |
| DevOps / infrastructure | 12 | 1 critical · 3 high · 4 medium · 4 low |

All references are `file:line` at a3cef740; re-verify anchors before fixing —
several PRs (#1972/#1973 and later) merged the same day.

## Cross-discipline top-15 fix shortlist

1. **Internal IPs in 61 tracked files** (DevOps-1, CRIT) — code/script fixes now;
   docs need the REPO-SIZE history decision; add an IP grep to hook + CI.
2. **ApplyVersionGroup / ApplyMultidisc integrity bugs** (Dedup F2, HIGH) —
   double-primary groups, stranded groups, soft-deleted "corpse" members can be
   re-linked or CombineBooks'd. Apply path is merged (#1953) behind a
   default-OFF switch — fix BEFORE the switch is flipped.
3. **Zombie-op chain** (Pipeline C-3 + C-2) — abandoned op keeps its running row
   and ConcurrencyKey; enqueue-dedupe then swallows every future enqueue of that
   def until restart. Fixing either half breaks the chain.
4. **RenameFiles phase-2 stranding** (Pipeline DL-1) — files left at
   `.tmp-rename` forever, moved files never get DB path updates.
5. **Wired move paths overwrite existing targets** (Pipeline DL-2/DL-3) —
   os.Rename/os.Create silently destroy another book's bytes on path collision;
   the safe MoveBookFile has zero production callers.
6. **Five scheduled iTunes ops are silent no-op stubs** (Logging C1) — two on
   cron report green every 10/30 minutes doing nothing.
7. **Watchdog blind to never-progressed ops** (Pipeline R-2 + DevOps
   observability gap) — an op that hangs before its first UpdateProgress is
   never detected; no "op went silent" alert exists. This is the 3-hour/9-hour
   silent-incident class.
8. **Remux/transcode ops silent for up to 6 h** (Logging C2) — reporter never
   threaded; errors can't fail the op.
9. **Cancel is a silent no-op for queued ops** (Pipeline C-1) — op runs anyway,
   possibly hours later.
10. **Engine.Rescore truncates at 100K pending** (Dedup F5) — silently rescored
    subset while claiming completeness; same class PR #1962 swept.
11. **`op.terminal` SSE never published** (Pipeline R-1) — completed ops linger
    as phantom "running" in the UI bell.
12. **Deploy freshness + template deploy** (DevOps-2/3) — no origin-freshness or
    post-deploy version check (stale-binary footgun); template deploy
    cross-compiles with embed_frontend but never builds web/dist.
13. **Pre-commit hook silently no-ops in linked worktrees** (DevOps-4) — 1-line
    fix (`--git-common-dir`), in a worktree-mandatory repo; plus no staged-content
    secret/IP scan.
14. **Silent AI retries** (Logging C6) — quadratic backoff sleeps with zero
    logging; a 429 storm or a down Ollama daemon masquerades as a hang.
15. **Deferred ITL location fixes falsely marked applied** (Pipeline DL-5) —
    dropped fixes never retried.

---

## 1. Dedup / identification subsystem (F1–F7)

Counts: 2 HIGH correctness (both confirmed) · 3 MEDIUM correctness (2 confirmed,
1 plausible) · 2 LOW perf/hygiene (confirmed).

### F1 — Dismissed candidates resurrect on every rescan/import (HIGH, confirmed) — **FIXED by PR #1973** (merged 2026-07-17, after the review's base commit)

- `internal/database/embedding_store.go:634-635` — `UpsertCandidateNew`
  existing-pair branch did `existing.Status = c.Status` unconditionally; empty
  incoming status defaults to "pending" (`:518-520`).
- Emitters passing `Status:"pending"` with no prior-status check:
  `internal/dedup/engine.go:1353` (upsertExactCandidate), `engine.go:1908`
  (findSimilarBooks — fired on every import via EventBookImported,
  `lifecycle.go:103`), `engine.go:1972` (CheckAuthor),
  `internal/scanner/scanner.go:113`, `internal/server/server_search.go:163`.
- Escalation: a resurrected pair rescored to CERTAIN re-entered the
  AutoResolveCertain funnel (`auto_resolve.go:104`) — a human-rejected pair
  could reach auto-merge.
- Fix shape (as shipped): never let incoming "pending" overwrite a terminal
  status (dismissed/merged). Verify current main before further action.

### F2 — ApplyVersionGroup (PR #1953 apply path) version-group integrity bugs (HIGH, confirmed)

`internal/plugins/maintenance/regroup_apply.go:102-170`:

1. Member B already in group G with non-member X (X primary) → apply reuses
   target=G, designates the hold's smallest-ULID member primary, X untouched →
   **two `IsPrimaryVersion=true` in one group** (invariant defended at
   `engine.go:3307-3321`).
2. Members in two different existing groups → the larger-ID group's member is
   moved out, stranding that group (possibly primary-less).
3. Soft-deleted members not filtered: `GetBookByID` returns MarkedForDeletion
   rows (documented `auto_resolve.go:355-359`); neither `presentMembers`
   (line 185) nor the version-group loop checks. Holds are created by the B1
   dry-run days before approval; a member merged away in between is still
   "present". ApplyVersionGroup can re-link the corpse and designate it primary
   (smallest ULID = oldest = likeliest merge-loser). ApplyMultidisc (line 61)
   will CombineBooks a corpse's files onto a survivor and hard-delete it even
   though a prior merge already reassigned its external IDs.

Fix shape: filter `bookSoftDeleted` in both apply paths; refuse when a member
carries a different non-empty VersionGroupID, or load all current group members
and demote existing primaries in the same pass. **Must land before
`review_apply_enabled` is flipped ON.**

### F3 — MarkCandidatesAsMergedForEntity bypasses the status index (MEDIUM, confirmed)

`embedding_store.go:1194-1256`: rewrites `rec.Status="merged"` committing only
`dedup:r:` — no delete of `dedup:s:pending:<id>`, no set of
`dedup:s:merged:<id>` (contrast `UpdateCandidateStatus:1096-1103`,
`UpsertCandidateNew:655-661`, `DeleteCandidate:1176`). Called after EVERY merge
via `CleanupCandidatesAfterMerge` (`engine.go:2763`). Indexed
`Status:"merged"` listings miss these rows; stale pending index rows grow
unboundedly (wasted point-read on each pending listing).

### F5 — Engine.Rescore truncates at 100K pending (MEDIUM, confirmed)

`engine.go:2816-2819` uses `Limit:100000` where siblings use 1_000_000
(`dataset_backfill.go:180`, `engine.go:1555`, `:2899`, `auto_resolve.go:108`).
`paginateCandidates` (`embedding_store.go:949-962`) hard-slices after a
similarity-desc sort → with >100K pending, Rescore silently rescores only the
top-similarity subset while `Inspected` claims completeness. Same class
PR #1962 swept; this instance remains.

### F6 — Legacy dedup.MergeBooks op skips external-ID reassignment and ITL removal (MEDIUM, plausible)

`internal/dedup/book_dedup.go:353-462`, reached from POST /audiobooks/merge
(`handlers/duplicates/handler.go:292` → `duplicates_ops.go:151`). Unlike
`merge.Service.MergeBooks` (`service.go:184-224`: collect PIDs →
ReassignExternalIDs → EnqueueRemove → soft-delete), the legacy path copies six
iTunes fields first-win then HARD-deletes losers (`store.DeleteBook`) — no
external-ID reassignment, no ITL removals, no recovery window. Unverified:
whether DeleteBook internally tombstones mappings. Mitigations: takes
`merge.LockMergeRMW` (line 360); `duplicates_ops.go:160` runs
`CleanupCandidatesAfterMerge`.

### F4 — Bulk delete paths leak dedup:e:/dedup:s: index rows (LOW-MED, confirmed)

`embedding_store.go:1293-1303` (`RemoveCandidatesForEntity` deletes only
`dedup:r:` + `dedup:p:`) and `:1367-1377` (`CanonicalizeCandidates`
duplicate-delete branch). Bounded to permanent index bloat; same
missing-index-maintenance class as F3.

### F7 — quarantine-chapter-artifacts serial whole-subset loops (LOW, confirmed)

`internal/plugins/dedup/quarantine_chapter_artifacts.go:121-162` (per-book
GetBookFiles, serial) and apply loop `:180-199` — mandated `registry.RunItems`
shape written as a plain `for`. (Apply-path soft-delete uses the correct
full-fetch-mutate pattern; no wipe risk.)

### Reviewed and sound

PairEligibility/hasPlausibleAudio; dataset.BuildExample/Classify;
dataset-backfill suppress-after-label-write ordering;
AutoResolveCertain/autoMergeCertain (kill switch, soft-delete pre-check,
journal-before-merge); UpsertCandidateNew new-pair batch atomicity;
merge.Service.MergeBooks/CombineBooks serialization + nil-override wipe safety;
iTunes albumGroupKey over-merge guard; full_scan.go/purge_stale.go pending-only
purge.

---

## 2. Pipeline / operations layer

Scope note: the op queue lives in `internal/operations/registry/`
(registry.go, dispatcher.go, worker.go, watchdog.go, resume.go, batch.go).
Counts: Data-loss 5 (2 confirmed, 3 plausible) · Correctness 7 (5 confirmed,
2 plausible) · Reliability 9 (7 confirmed, 2 plausible) · Perf 2.

### Data-loss

- **DL-1 (confirmed)** `internal/organizer/pipeline.go:194-203` — RenameFiles
  phase-2 failure strands files at `.tmp-rename` with no rollback; retry
  `os.Stat(SourcePath)` buckets them into Skipped forever; both callers discard
  `result.Succeeded` so already-moved files never get DB path updates.
- **DL-5 (confirmed)** `internal/itunes/service/importer.go:502-521` — deferred
  ITL location fixes marked applied for ALL pending rows when only a filtered
  subset was written; `RenameITLFile` error discarded; dropped fixes never
  retried.
- **DL-2 (plausible)** `organizer/service.go:454`, `rename.go:392,450,480` —
  wired move paths have no target-exists check; `os.Rename` silently replaces →
  destroys another book's bytes on path collision. The safe `MoveBookFile`
  (`move.go:42-45`) has zero production callers.
- **DL-3 (plausible)** `organizer/reflink_unix.go:26` — `os.Create` truncates an
  existing destination; stat→create TOCTOU under the 8-worker organize pool.
- **DL-4 (plausible)** `scanner/scanner.go:2102` —
  `file.Seek(-chunkSize, io.SeekEnd)` return discarded → wrong-window hash on
  seek failure poisons dedup (sibling `process_file.go:123-125` checks
  correctly).

### Correctness

- **C-1 (confirmed)** `registry/registry.go:773-793` + `dispatcher.go:130-136`
  — Cancel is a silent no-op for an op sitting in the buffered nextRun channel
  (stub handle nil cancel; queued-path DB cancel never attempted); the op runs
  anyway, possibly hours later.
- **C-2 (confirmed)** `registry/worker.go:139-143` — checkInfiniteRestart
  force-drop returns without releaseRunHandle: ConcurrencyKey held forever
  (blocks all 6 defs sharing "acoustid.fingerprint"), plugin slot leaked, until
  restart. Doc comment claims "handle released" — it is not.
- **C-3 (confirmed)** `worker.go:299-314` + `registry.go:551-561` — an
  abandoned op never gets a terminal status; the row stays running in the active
  index; ConcurrencyKey enqueue-dedupe (matches DefID only) returns the zombie's
  ID for every future enqueue — the op type is silently disabled until restart.
- **C-4 (confirmed)** `watchdog.go:110-130` — the uncheckpointed strike compares
  against a 5m constant instead of `def.MinCheckpointInterval`; re-inserts the
  strike row every 30s with no dedupe.
- **C-5 (confirmed)** `worker.go:154,260-267,325-334` — `def.Timeout` expiry is
  recorded as `canceled` (indistinguishable from user cancel); the dep scheduler
  is notified only on completed/failed → waiting subjects fall back to the 5m
  sweep.
- **C-6 (confirmed)** `internal/itunes/service/importer.go:417-425` —
  blocked-hash soft-delete UpdateBook return ignored (sibling at `:438` checks);
  `hashBlocked++` and the log claim a delete that may not have happened.
- **C-7 (plausible)** `itunes/service/importer.go:1199-1229` — multi-file
  rollback reverts only `Book.FilePath`; committed per-file UpdateBookFile
  writes are not reverted → DB/disk inconsistent.

### Reliability

- **R-1 (confirmed)** SSE gap — `op.terminal` is in the event contract
  (`bus.go:15`, `web/src/services/api.ts:501`, `useOperationsStore.ts:352`) but
  there are ZERO backend publishers; terminal writes at `worker.go:230,336`
  publish nothing → completed ops linger as phantom "running" in the UI bell.
- **R-2 (confirmed)** `watchdog.go:89-102` — stuck detection requires a non-zero
  progress timestamp; marking an op running doesn't stamp one
  (`pebble_store_ops_v2.go:224-247`); an op that hangs before its first
  UpdateProgress is never detected — the "silent for hours" class. Fix: fall
  back to `row.StartedAt`.
- **R-3 (confirmed)** `reporter_db.go:217-245` + `worker.go:162` — after
  abandonment, the reporter flushLoop exits (runCtx canceled) while the wedged
  Run keeps calling Log; logBuf grows unbounded, every line silently lost.
- **R-4 (confirmed)** `scanner/scanner.go:161-231` vs `service.go:161-168` —
  globalScanCache/worksLookupCache are package singletons cleared by defer per
  run, but library.scan and library.import may run concurrently (distinct
  ConcurrencyKeys) through the same code path; the first finisher nils the
  caches under the other → incremental skip disabled, works lookup O(N²)
  mid-run.
- **R-5 (confirmed)** `scanner.go:390-396, 370-373, 433-436` —
  WalkDir/ReadDir/stat failures silently drop whole subtrees, zero logging.
- **R-6 (confirmed)** `internal/reconcile/reconcile.go:1270-1327` (via
  `server/reconcile.go:178-185`) — AssignOrphanVGs: serial
  2-DB-calls-per-book whole-library loop (unpooled) and unconditionally
  overwrites VersionGroupID on the re-hydrated book without re-check — clobbers
  concurrent VG assignments.
- **R-7 (confirmed)** `scanner/service.go:73-83` — scan "checkpoint support"
  saves ScanParams no code ever loads (`LoadParams[ScanParams]` zero callers);
  ClearState unconditional; a crash mid-scan resumes nothing.
- **R-8 (plausible)** `scanner/chapter_consolidation.go:103-106` — mediainfo
  failure leaves duration 0; an all-unreadable group averages 0 < threshold →
  consolidated as "short" when duration is actually unknown.
- **R-9 (plausible)** `itunes/service/path_repair.go:218-317` — main track loop
  fully sequential per-track DB read/write (concurrency-mandate shape); reports
  every 500 tracks so won't go silent.

### Perf

- **P-2** `registry/run_items.go:114` — parallel RunItems reports item index,
  not completion count; progress can jump backwards (cosmetic).

### Compounding chain

Stuck op → watchdog cancel (only if it ever reported progress, R-2) → wedged
goroutine → abandoned with no terminal status (C-3) → zombie running row →
ConcurrencyKey dedupe swallows all future enqueues of that def until restart.
Fixing either half of C-3 (write `interrupted_*` on abandonment, or make dedupe
ignore rows with no live handle) breaks the chain.

### Checked clean

registry/batch.go (journal-first, gen-counter, fireWG drain); shutdown
sequencing; dispatcher Gate-0 stub + write-lock re-check; EventHub locking;
resume.go; RunItems cancellation; organizer/checkpoint.go;
itunes/writeback_batcher.go (backup→temp→validate→rename→re-validate + circuit
breakers); iTunes write-back audited for the memdb full-replace
fingerprint-wipe class — all hydrate via full-fidelity getters;
scanner/process_file.go; ProcessBooksParallel cancellation; registry_wire.go.

---

## 3. Logging coverage (C1–C7, H1–H9, M1–M8)

Well-instrumented paths verified and excluded: `dedup.full-scan` (per-phase
progress + ETA), `book-signature-scan`, `itunes-heal` main loop (RunItems +
counters), `online-lookup`, `calibrate-scoring`, orphan-book-files delete pass,
scanner's `ProcessBooksParallel` (logs every 100 files).

### Critical

- **C1 — Five registered iTunes ops are silent TODO stubs, two on cron.**
  `internal/plugins/itunes/sync.go:35-39` (`itunes.sync`, schedule
  `*/30 * * * *`), `position_sync.go:35-39` (`itunes.position-sync`,
  `*/10 * * * *`), `import.go:36-38`, `path_reconcile.go:36-38`,
  `path_repair.go:34-36`. Each `Run` is `// TODO` + `return nil` — zero log
  lines; `itunes.sync` "completes" green every 30 min doing nothing. Until
  implemented: log a `stub_skip` Warn per run — or drop the Schedule.
- **C2 — malformed-m4b remux/transcode: up to 6 h of ffmpeg, one op-log line.**
  `internal/plugins/maintenance/backfill.go:90-95` (remux, 120 min) and
  `:117-122` (transcode, 6 h timeout) log "Starting…" then call
  `p.deps.RemuxMalformedM4BFiles(ctx)` — the reporter is never passed down.
  Impls (`internal/remux/remux.go:48-118`, `internal/remux/transcode.go:48-141`)
  walk every `.m4b` under RootDir with per-file ffprobe/ffmpeg, slog-logging
  only remuxed/failed files + a final summary — a healthy library run is silent
  for hours, and only to journald, never the op log. Deps also return void
  (`internal/server/malformed_m4b_wrappers.go:16-26`) so failures can't fail
  the op. Fix: thread reporter/callback; UpdateProgress every N files.
- **C3 — movement-atom-cleanup: whole-library WalkDir + taglib rewrite, no
  heartbeat.** `internal/server/movement_atom_cleanup.go:33-88` (op wrapper
  `backfill.go:65-70`). Start log, then a full walk with per-error logs only;
  no periodic progress, reporter not passed. Also `_ = filepath.WalkDir(...)`
  swallows the walk error and `_ = store.SetSetting(movementAtomCleanupKey, …)`
  swallows the done-flag write — a failed flag write silently re-runs the
  entire walk next boot.
- **C4 — DedupTriageExactPending: up-to-1M-candidate loop, zero logging.**
  `internal/server/server_maintenance_deps.go:260-351`.
  `ListCandidates(Limit: 1_000_000)` then a serial classify loop with per-book
  `GetBookByID`; no start/progress/complete logs;
  `b, _ := s.Store().GetBookByID(id)` (line 280) swallows DB errors, silently
  skewing the triage report. Also violates the concurrency mandate. Fix:
  progress every 5-10K + an error counter. (Load-bearing: this is the PH-2
  triage op.)
- **C5 — dedup.split-book-scan silent for the whole detection pass.**
  `internal/plugins/dedup/split_book_scan.go:50-56` →
  `internal/dedup/split_book_detector.go:92-140`. `prog.Start("Scanning
  library…")` then `DetectSplitBookCandidates` paginates ALL books +
  `GetAuthorByID` per author with no progress callback (unlike siblings
  FullScan/BookSignatureScan). Silent up to the 60-min timeout. Fix: `progress
  func(loaded int)` in `loadSlimBooks`, StepN per 1K page.
- **C6 — `internal/ai/retry.go:66-90` DoWithRetry: silent retries for ALL
  OpenAI/Ollama calls.** Quadratic backoff sleeps (minutes) with no log of
  error, attempt, or backoff. A 429 storm or a down Ollama daemon looks like a
  mysterious crawl — the network-bound twin of the 3-hour-silent incident. Fix:
  Warn per retry with attempt/max/backoff/err.
- **C7 — dedup unified scoring: destructive candidate deletion logged
  Debug-only, no aggregate.** `internal/dedup/engine.go:591-600`. Suppressed
  pairs are DELETED (`DeleteCandidate`) with per-pair Debug only; delete
  failures also Debug. The `suppressors` reasons from PairEligibility
  (`internal/dedup/eligibility.go:64`) are discarded. Fix: accumulate
  `suppressedByReason map[string]int` + deleteFailures, one Info summary at
  scan end.

### High

- **H1.** `internal/dedup/engine.go:574` and `:805-811` —
  GetBookByID/GetBookByFileHash err → bare `continue`; a failing store makes a
  scan quietly score nothing. Add an err counter + one Warn summary.
- **H2.** `internal/dedup/author.go:745-752` (+ `:759`) —
  GetBooksByAuthorIDCore/GetSeriesByID err → silent continue while building the
  all-authors series map; degrades author-dedup with zero evidence.
- **H3.** `internal/reconcile/itunes_heal.go:366-371, 379-381, 476` — fpcalc,
  `ac.Lookup` (AcoustID), and Whisper failures all `continue` — an API outage
  is indistinguishable from "no match" and silently inflates `ambiguous`. Add
  per-resolver failure counters into the existing RunItems Label +
  rate-limited Warn.
- **H4.** `internal/itunes/service/writeback_batcher.go:409` (also
  `position_sync.go:162`, `importer.go:398`) — GetBookByID err treated as a
  legit skip; store errors at flush time silently drop iTunes metadata writes.
  Log errors distinctly from nil-book skips.
- **H5.** `internal/scanner/scanner.go:1844-1847` — duplicate-detection hash
  lookups: err → continue → possible silent re-import of an existing book;
  `:614`/`:867` `_ = store.UpdateScanCache(...)` (file re-hashed every scan
  forever, silently); `:733` `_, _ = gs.IncrScanFailCount(...)`.
- **H6.** `internal/scanner/service.go:242` — `_ = filepath.WalkDir` in the
  count phase ignores walk errors (callback also drops its err arg); a
  permission error undercounts the scan-progress denominator. Log the first
  walk error per folder.
- **H7.** `internal/plugins/maintenance/backfill.go:40-45` →
  `internal/server/external_id_backfill.go:47-65` — the domain fn returns an
  error but the wrapper demotes it to Warn and returns void; the op logs
  "complete" unconditionally. Reporter not threaded → no progress on the
  whole-library pagination inside `internal/itunes/backfill.go:37`.
- **H8.** `internal/plugins/maintenance/author.go:167-172` — GetBookAuthors err
  → continue in the author-merge path (data-affecting miscounts).
- **H9.** `internal/plugins/dedup/llm_review.go:35-49` +
  `internal/dedup/engine.go:3114-3124` — slog start/queued/submitted lines
  exist, but the op-log shows nothing between Start and submit while building
  up to 10K pair inputs (2 GetBookByID each) on a 120-min-timeout op. Wire
  `sdk.NewProgress` + StepN every 100.

### Medium

- **M1.** `internal/plugins/maintenance/transcribe_stats_accum.go:112` —
  `_ = a.sink.PutTranscribeStats(...)`: the live-monitor key can go stale with
  no warning.
- **M2.** `internal/dedup/collectors_metadata.go:401` GetBookByID err →
  continue, no counter; `:240-253` four `_ = EnsureSingletonBookTag(...)`
  (user-visible dedup evidence tags dropped silently).
- **M3.** `internal/dedup/engine.go:1247-1267` — four more swallowed
  EnsureSingletonBookTag calls.
- **M4.** `internal/organizer/pipeline.go:187` —
  `_ = os.Rename(t.TempPath, t.Entry.SourcePath)` in the ROLLBACK path: a
  failed rollback strands a file at the temp path with no log. Log at Error
  with both paths.
- **M5.** `internal/itunes/service/importer.go:316, 358-360` —
  `_ = CreateExternalIDMapping` / `_ = SetBookAuthors` during import; `:1648`
  DecodeLocation err → continue, no malformed-location counter.
- **M6.** `internal/itunes/service/path_repair.go:244` — undecodable locations
  skipped with no count in the repair summary.
- **M7.** `internal/plugins/maintenance/metadata.go:66-80` +
  `server_maintenance_deps.go:124` MetadataUpgradeRun — books × external
  metadata fetches with no progress between the start line and the single
  result line (network-bound, 30+ min shape).
- **M8.** `internal/scanner/scanner.go:402` — `_ = registerDirectory(path,
  info)` (watcher coverage failures invisible).

### Top-10 worst gaps (logging discipline's own ranking)

1. `internal/plugins/itunes/sync.go:35` + 4 siblings — scheduled ops are silent
   no-op stubs, green runs every 10/30 min (C1)
2. `internal/plugins/maintenance/backfill.go:117` →
   `internal/remux/transcode.go:48` — 6-h transcode op, one op-log line, errors
   can't fail it (C2)
3. `internal/plugins/maintenance/backfill.go:90` →
   `internal/remux/remux.go:48` — whole-library ffprobe walk, no periodic
   progress (C2)
4. `internal/server/movement_atom_cleanup.go:33` — library-wide tag rewrite, no
   heartbeat, done-flag write swallowed (C3)
5. `internal/ai/retry.go:66` — every AI call retries silently; network stalls
   masquerade as hangs (C6)
6. `internal/server/server_maintenance_deps.go:260` — 1M-candidate triage loop,
   zero logs, swallowed DB errors (C4)
7. `internal/dedup/split_book_detector.go:92` — all-books detection pass, no
   progress callback, silent up to 60 min (C5)
8. `internal/dedup/engine.go:591` — destructive suppression deletes at Debug
   only, no per-scan skip summary (C7)
9. `internal/reconcile/itunes_heal.go:379` — AcoustID/fpcalc/Whisper failures
   look like "no match" (H3)
10. `internal/scanner/scanner.go:1844` — dup-detection hash lookups swallow
    store errors → possible silent re-import (H5)

---

## 4. DevOps / infrastructure (findings 1–12)

### 1. CRIT — Internal IPs present in 61 tracked files

Internal fleet IP addresses (prod host + GPU box) appear in 61 tracked files
(addresses deliberately not reproduced here — see the private-knowledge routing
rule). Notable hits: shipped CLI help (`cmd/dedup_bench.go:74`),
`docs/system/runbooks.md` (12 occurrences), `scripts/transcribe_monitor.py`,
`scripts/dedup_bench_submit.py`, `scripts/setup-winrm-windows.ps1`, TODO.md
(**cleared by the 2026-07-17 live-TODO rewrite**; the frozen archive copy still
carries history), `agents/pii-scanner.md` (uses the real prod IP as its PII
example), `skills/project-context/SKILL.md`,
`tools/cmd/reconcile-paths/main.go`, plus dozens of docs and a second internal
address in archived plans. Templates ARE clean (`Makefile.local.example`,
`deploy/local.conf.example` use RFC-1918 placeholder addresses from a different
range). No sandbox-mechanism leakage found in tracked files.

Remediation: fix code/scripts now (env-var the base URL); docs history requires
the REPO-SIZE-1 decision (history rewrite vs forward-only); add an IP-pattern
grep to the pre-commit hook and a CI backstop.

Also verified: SHA-pinning 0 violations across 15 workflows; live tokens/keys
0 found.

### 2. HIGH — Deploy chain gaps

- **2a.** `Makefile.local` deploy: no origin-freshness check
  (`git merge-base --is-ancestor origin/main HEAD` pre-flight), no refusal of
  `-dirty` builds, no post-restart `curl /api/v1/system/version` vs
  `git describe` — the stale-binary footgun remains open (~10 lines to fix).
- **2b.** `Makefile.local.example:30-52` BROKEN: deploy depends on build-api
  then cross-compiles with `-tags embed_frontend` WITHOUT web-build
  (stale/absent web/dist); omits fts5/native_taglib/CGO-musl static-link;
  ships only the binary (the real deploy ships unit + drop-in too) (~5 lines).
- **2c.** `make backup`/`rollback` well-designed but lack a post-restart health
  check.
- **2d.** The wipe-deploy/fresh-import chain is a full prod wipe one
  target-typo from deploy; add an interactive gate.

### 3. HIGH — CI/CD gaps

Good: 100% SHA-pinned; the PR gate = Minimal CI (vet/build, short+race,
frontend) + Mock Freshness (mockery v3.7.1 pinned) + Binary Smoke (real boot +
health). Gaps:

1. `embed_frontend` is never built pre-merge — the first embedded build happens
   in prerelease.yml AFTER merge; fix = one step in binary-smoke.
2. The coverage floor (30, `.ci/coverage-floor.txt`) is not enforced on the PR
   path — only local `make ci` + nightly.
3. The flaky "Go Tests (short, race)" stalls have no automation.
4. No hard blockers for a fix wave; friction: mocks-check on interface changes,
   the intermittent race stall.

### 4. HIGH — Pre-commit hook silently no-ops in linked worktrees

`scripts/setup-git-hooks.sh:8` uses `--git-dir`; should be `--git-common-dir`
(1-line fix) — in a worktree-mandatory repo the hook install silently does
nothing from any worktree. Additional gaps: path-match only, no staged-content
scan — add a grep for API-key and internal-IP patterns (would catch the
PR #1974 leak class); bypassable with `--no-verify` and no CI backstop runs
automatically.

### 5. MED — embed_frontend not built pre-merge (see 3.1; 1 CI step).

### 6. MED — No "op went silent" alert

`deploy/prometheus/` has 5 well-reasoned alerts but nothing for the
op-stall class (the 3-hr and 9-hr incidents): needs an op-progress metric
export + a `rate(items_processed)==0`-while-active-30m rule. Related gaps: the
AI-backend gauge is startup-only (misses mid-session Ollama outages), and it is
unverified whether Prometheus/Alertmanager are actually deployed on the prod
host. `docs/observability/` (OTel setup, opt-in) is accurate.

### 7. MED — Coverage floor not on the PR gate (see 3.2).

### 8. MED — Zero sandbox tooling in the repo; ops scripts hardcode the prod URL

Nothing in the repo can target the dedup sandbox without editing hardcoded prod
URLs (two scripts). Proposal: a public-safe `scripts/op_verify.py`
(`--base-url --token-file --op DEF_ID [--dry-run] [--expect-json]`: trigger,
poll, pull logs, before/after counts, non-zero exit on failure/mismatch) — the
ops API (`POST /api/v1/operations/v2`, poll, logs, `/dedup/candidates`,
`/dedup/stats`) is the full needed surface; refactoring the two hardcoded-IP
scripts to take `ABK_API_URL` fixes a leak AND enables sandbox targeting.
Private `Makefile.local` targets then deploy a branch binary to the sandbox and
run op_verify against it. CI can't reach the sandbox → extend binary-smoke into
a dedup-smoke (ephemeral instance seeded from testdata/, `dedup.full-scan`
dry-run on localhost): CI catches "op crashes"; the sandbox catches "op is
wrong against real data". Per-PR wave workflow: `make ci` → branch binary
(beware the LOCAL_ROOT stale-worktree footgun) → sandbox deploy → dry-run
(record counts vs the 15,269/9,074 baseline) → sandbox apply →
isolation/aftermath check → merge → `git pull --ff-only` + `make deploy` (with
version-verify) → prod dry-run diffed vs sandbox dry-run → human-gated prod
apply. Re-clone the sandbox from a fresh snapshot before each wave.

### 9. LOW — Hook content scan (see 4).

### 10. LOW — Credential entropy/echo

`manage-credentials.sh`: ~27-bit passwords (3 words + 4 digits); secrets echoed
to stdout. Use `openssl rand -base64 15`; print the file path, not the secret.
`.gitignore` verified complete for credential files.

### 11. LOW — Duplicate systemd unit files

`deploy/` vs `deploy/systemd/` drift risk. The drop-in pattern itself is
correct (service v4.4.0, sane hardening, MemoryMax=12G > GOMEMLIMIT=9GiB
documented).

### 12. LOW — Flaky race-test stalls remain manual (see 3.3).
