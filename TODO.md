<!-- file: TODO.md -->
<!-- version: 9.84.0 -->
<!-- guid: 8e7d5d79-394f-4c91-9c7c-fc4a3a4e84d2 -->
<!-- last-edited: 2026-07-10 -->

# Project TODO

Canonical index into every piece of outstanding work across the project.
Details live in the linked files; this file exists so anyone (you, me, a
future agent) can scan the entire workspace in one page.

**Sources indexed here:**
- [`docs/agent-tasks/`](docs/agent-tasks/) — **manual AI-agent task package** (run by hand, not the burndown bot): self-contained briefs + orchestration scripts
- [`docs/backlog-2026-04-10.md`](docs/backlog-2026-04-10.md) — 1725-line working list, ranked by category
- [`docs/superpowers/plans/`](docs/superpowers/plans/) — implementation plans per feature
- [`docs/superpowers/specs/`](docs/superpowers/specs/) — design specs per feature
- [`docs/implementation-guide.md`](docs/implementation-guide.md) — integration guide for open items
- [`docs/codebase-evaluation.md`](docs/codebase-evaluation.md) — 2026-04-30 codebase audit (12 issue groups, 38 bot-tasks)
- Claude project memory at `~/.claude/projects/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/memory/` — items still to graduate here

---

## 📦 2026-07-10 Remaining-Work Planning Packages — READY FOR EXECUTION

Ten full planning packages (spec + plan + task briefs) covering INIT-1..10 of the
remaining-work catalog, produced, adversarially judged (3 lenses × 10), brief-verified
(50 cold-executor role-plays), and mechanically audited on 2026-07-10.

- **Entry point (read this first):**
  [`docs/plans/2026-07-10-execution-manifest.md`](docs/plans/2026-07-10-execution-manifest.md)
  — per-initiative spec/plan/tasks paths, gates, cross-initiative constraints
  (engine.go partition, metadata_ops.go serialization), recommended execution order
  (Phase A parallel-autonomous → Phase B post-merge → Phase C human decision points),
  and the out-of-catalog TODO appendix.
- Executable now (autonomous lane): INIT-2, INIT-3, INIT-4, INIT-9 (minus REPO-SIZE-1),
  INIT-10 (minus C8), INIT-1 waves W1–W3, INIT-5 T1.
- Human decision points: INIT-5 T2 Deluge spike sign-off; INIT-6 + INIT-8 spec reviews
  (STOP-FOR-HUMAN); INIT-9 REPO-SIZE-1 history-rewrite plan; INIT-7 hold-lift (#1260–#1265).
- Prod-data mutations (INIT-1 T7, INIT-2 T3/T6 drains, INIT-10 C8) are dry-run →
  AskUserQuestion gated in the briefs.

---

## ✅ Author embeddings stranded on stale model (bge-m3 cutover follow-up) — FULLY RESOLVED (2026-07-08)

- **Found via prod journalctl**: every restart since the Jul 2 2026 local-embeddings cutover
  (OpenAI text-embedding-3-large 3072-dim → bge-m3 1024-dim), `HydrateChromem` logged
  `dedup chromem upsert author ... vector dim 3072 != store dim 1024` for ~3,450 authors
  (10,350 warnings in one startup burst; confirmed not ongoing — no recurrence since).
- **Root cause**: `dedup.reembed-embeddings` (built for this exact cutover) is books-only by its
  own doc comment ("re-embedding authors is left to a follow-up" — never built).
  `runEmbeddingBackfill`'s author loop is already model-aware (PR #1744) but gated by
  `BackfillVersionMarker`, which predates the cutover and was never bumped, so it never re-ran.
- **Fix**: bumped `BackfillVersionMarker` v5 → v6 (`internal/dedup/backfill_progress.go`) — one
  constant, zero new code, reuses the existing tested concurrent author-embed path. Deployed and
  verified on prod: 9,080/9,083 authors reconciled, warning count dropped from 10,350/restart to 0.
- **Residual (corrected)**: 3 authors (39755, 40861, 42076) still warned post-v6. Initially assumed
  a `GetAllAuthors()` snapshot race and bumped v6 → v7 to retry — but the same 3 IDs recurred
  identically after v7, proving it wasn't a race. Root cause: `GetAllAuthors()` iterates literal
  `author:N` Pebble keys and returned `total=9080` both times, while the embedding store has 9083
  rows — these 3 are orphaned rows left by an author merge/delete (`GetAuthorByID` has
  tombstone-redirect logic for merged authors that `GetAllAuthors()` doesn't apply). No backfill
  re-run can ever reach them, since the entity is gone.
- **Final fix**: `HydrateChromem` (both book and author loops) now skips any row whose stored model
  doesn't match the current embed client, instead of attempting a doomed mirror + warning. Logs one
  summary line per hydrate (`stale_books`/`stale_authors` counts) so orphaned/stale rows stay
  visible instead of silently vanishing. No marker bump needed — deploy + restart only.
- **Follow-up shipped + run**: `dedup.cleanup-orphan-author-embeddings` op built (author-side
  counterpart to the existing book op) and deployed. Had to diverge from the book op's
  `GetBookByID(id) == nil` pattern — `GetAuthorByID` follows the same tombstone redirect that caused
  the original bug, so the op checks existence against `GetAllAuthors()` instead. Covered by a
  regression test that reproduces the redirect.
- **Final closure (prod, 2026-07-08)**: dry-run confirmed exactly the predicted 3 orphans
  (39755, 40861, 42076; 9080 live, 9083 total) — apply deleted **3/3, 0 errors**. Idempotency
  re-check: dry-run now reports **0 orphaned, 9080 live, 9080 total**. Fresh restart's
  `chromem hydrate` summary line confirms **stale_authors=0**. The whole saga — 10,350
  warnings/restart → 0, plus the underlying dead rows physically removed — is fully closed across
  PRs #1862 (backfill v6), #1865 (v7, proved it wasn't a race), #1866 (hydrate model-guard), #1867
  (orphan-cleanup op).

---

## 🟠 CreateOrganizedVersion original-book slim-writeback (Author/Series) — follow-up (STOREFID W5d-1, 2026-07-07)

- **Pre-existing latent bug** surfaced during STOREFID W5d-1 review.
  `internal/organizer/service.go` `CreateOrganizedVersion` writes the page-sourced
  *original* book back with the version-group / non-primary / `organized_source` stamp:
  ```go
  book.VersionGroupID = &versionGroupID
  book.IsPrimaryVersion = &isNotPrimary
  book.LibraryState = &organizedSourceState
  orgSvc.db.UpdateBook(book.ID, book)   // book is GetAllBooksCore→ToBook, heavy-field-nil
  ```
  Under prod's memdb default `book` has nil `Author`/`Series` (not STOR-1-guarded), so this
  wipes the original's denormalized author/series. Prod behavior is unchanged by W5d-1
  (memdb already stripped these); the `.ToBook()` in W5d-1 just makes it no longer
  compile-visible — `GetAllBooks` was removed entirely in W5z (2026-07-07), so this is now
  the only remaining route back to a full `Book` in this code path.
- **Fix must be careful:** hydrating via `GetBookByID` before the write (the usual pattern)
  preserves Author/Series BUT must NOT regress the version-group state transition to
  fail-closed — a `GetBookByID` error must still demote the original to non-primary, or the
  version group ends up with **two primaries**. So: hydrate-and-write on success, but fall
  back to the direct state write (accepting the rare Author/Series wipe) if hydrate fails —
  i.e. fail-OPEN for the state transition, preserve-heavy-when-possible. Add a regression
  test asserting Author survives AND the original is demoted even when GetBookByID errors.
- Same writeback shape as the W5d-1 organizer writebacks that DID get the `hydrateAndUpdateBook`
  helper; this one was deliberately left out pending the fail-open design above.

## ✅ PR-D: deluge import fingerprint-wipe (3 impls) — RESOLVED (STOREFID W6, 2026-07-07)

- **Fixed as a side effect of STOREFID W6's `GetBookFilesNeedingDelugeImport` → Core retype**:
  the narrower `BookFileCore` return type made the `UpdateBookFile(bf.ID, bf)` writeback in all 3
  impls a compile error, forcing (not just documenting) the hydrate fix at the same time as the
  retype. Fixed via `GetBookFileByID(bookID, fileID)` hydrate-mutate-update at each call site:
  `internal/plugins/deluge/centralization.go`, `internal/server/deluge_discovery.go` (before
  calling `deluge.ImportToLibrary`), `internal/maintenance/jobs/bulk_deluge_import.go` (before
  calling `bdi_importToLibrary`). `ImportToLibrary`/`bdi_importToLibrary` signatures unchanged.
- Regression test: `internal/plugins/deluge/centralization_test.go`
  `TestRunCentralization_HydratesBeforeWriteback` covers the `centralization.go` path only
  (seeds `FingerprintDiagnosticJSON`, asserts it survives `UpdateBookFile`; manually verified to
  fail against the pre-fix naive writeback). The other 2 impls (`deluge_discovery.go`,
  `bulk_deluge_import.go`) share the identical hydrate pattern but do not yet have their own
  regression tests — same shape, could be added as a follow-up if one of them regresses.
- Root cause + census (historical): `docs/audits/2026-07-05-updatebookfile-memdb-writeback-fingerprint-wipe.md`.

## 🟠 GetDuplicateBooksByMetadata is still a no-op stub in production (found 2026-07-07, folder half fixed 2026-07-10)

- **NEW finding** during STOREFID W6's Core retype of these 2 getters. `PebbleStore.GetFolderDuplicatesCore`
  and `PebbleStore.GetDuplicateBooksByMetadataCore` (`internal/database/pebble_store.go`) were both hard
  `return nil, nil` stubs — **no `MemStore` implementation existed for either getter**, so unlike
  every other slim getter in this codebase, these two ALWAYS returned empty regardless of
  `UseMemDB`. Folder-based duplicate detection (same title, same folder — e.g. M4B + MP3 pairs) and
  metadata-based fuzzy duplicate detection were therefore non-functional in production; only
  hash-based duplicate detection (`GetDuplicateBooks`) actually worked.
  Callers: `internal/dedup/book_dedup.go` (`ScanBookDuplicates`, tiers 2 and 3 of the 3-tier scan),
  `internal/audiobooks/service_single.go` (`GetDuplicateBooks`).
- **✅ Folder half FIXED 2026-07-10 (INIT-2 TASK-01)**: `PebbleStore.GetFolderDuplicatesCore` now
  delegates to a real `MemStore.GetFolderDuplicatesCore` twin when memdb is published, with a
  Pebble-scan fallback (paged `GetAllBooksCore` + per-book `GetBookFiles`) for cold start/tests.
  Both backends bucket non-deleted, primary-version books by
  `(util.NormalizeTitle(title), single-parent-dir)` via a shared `bucketFolderDuplicates` helper so
  the two paths can't drift. A book with no files, or files spanning multiple dirs, has an
  UNKNOWN parent dir and is silently skipped (never grouped, never an error). Dedup tier 2 in
  `ScanBookDuplicates` / `AudiobookService.GetDuplicateBooks` now returns real folder-duplicate
  groups. Tests: `internal/database/pebble_store_folder_dups_test.go` (both backends, incl. the
  anti-over-suppression case where a multi-dir book is skipped but other groups still return).
- **Metadata half still open (TASK-02, same files, later wave)** — implementing real
  metadata-based fuzzy duplicate detection is a separate scoped task: it already has full
  fuzzy-matching logic downstream (`applyTranscriptionMetadataTiebreaker`,
  `metadataPairSimilarity`) that's just never fed any candidates today.

## ✅ DurationSec invariant for the 3 PR-B fingerprint ops — RESOLVED + PROD-CONFIRMED (2026-07-06 → 08)

- The 3 rerouted ops (lsh_backfill / lsh_index_build / online_lookup) gate on
  `AcoustIDFingerprintDurationSec > 0` as the memdb-safe proxy for "has a whole-file
  fingerprint". This assumed `AcoustIDFingerprint set ⇒ DurationSec > 0`.
  Code comment in `internal/plugins/acoustid/online_lookup.go`.
- **Diagnostic added (2026-07-07):** `AcoustIDStats.WithFingerprintZeroDuration`, surfaced on the
  existing `GET /maintenance/acoustid-stats` endpoint.
- **Prod query run (2026-07-07, post-deploy of `b28e9d9e`): 2,781 of 296,010 fingerprinted rows
  (0.94%) violated the invariant.**
- **Fix built (2026-07-07):** new manual-trigger op `acoustid.duration-backfill`
  (`internal/plugins/acoustid/duration_backfill.go`) scoped to exactly these rows via the new
  `Store.GetFilesWithZeroDurationFingerprint` getter. Dry-run by default (`live` param must be
  explicitly `true` to write); bounded worker pool matching `fingerprint_rescan.go`.
- **✅ RESOLVED (2026-07-08):** deployed (commit `1194c726`), dry-run triggered on prod first
  (confirmed 2,781 affected, sample reviewed), then run live
  (op `01KWZW9ZGB5EP537643BAESPXR`): **2781/2781 fixed, 0 failed, 0 ineligible, 1m9s.** Re-queried
  `/maintenance/acoustid-stats`: **`with_fingerprint_zero_duration` dropped from 2781 → 0.**
  Invariant now holds across the full library. The 3 PR-B ops and `acoustid.backfill` can now see
  every fingerprinted row via the `DurationSec` proxy.

---

## ✅ dedup.full-scan freezes in composing-scores phase even after CONC-2/PR #1809 (2026-07-06) — RESOLVED + PROD-CONFIRMED 2026-07-07

- **✅ ROOT CAUSE (2026-07-07, static trace):** per-candidate `EmbeddingStore.UpsertCandidate`
  (`UpsertCandidateNew`) took the store-wide `s.mu` **and held it across
  `b.Commit(pebble.Sync)` — a synchronous fsync**. Every `NumCPU` score worker
  serialized behind that one lock+fsync (a `sync.Mutex.Lock` wait is not
  ctx-cancellable → why graceful cancel did nothing), and the per-write fsyncs
  flooded Pebble L0 until compaction fell behind (amplified by swap) and Pebble's
  **write-stall** froze all DB reads/writes → workers parked inside Pebble, 9+ h,
  hard-restart-only. The two named suspects (`de.mergeMu`, Ollama-ignoring-ctx) were
  **ruled out**: mergeMu guards only the scan phase; the embed client is already
  ctx-bounded and the score phase makes no network calls.
- **✅ FIX SHIPPED:** per-row candidate writes (`UpsertCandidateNew` ×2,
  `DeleteCandidate`) → `pebble.NoSync` (`candidateWriteOpts`). Correctness-identical
  (only fsync durability changes); graceful restart still loses nothing (WAL flushes
  on Close — `TestUpsertCandidate_SurvivesGracefulClose`); hard-crash loses only the
  last few seconds of *recomputable* scores. Guards: durability + `-race`
  concurrency test. Full write-up:
  [`docs/audits/2026-07-07-dedup-fullscan-composing-scores-writestall.md`](docs/audits/2026-07-07-dedup-fullscan-composing-scores-writestall.md).
- **⏳ GATE before closing #19:** `make deploy-debug` (pprof) → re-run `dedup.full-scan`
  → capture periodic goroutine+heap dumps → require a **clean full completion** (also
  clears the ~10,114 backlog). Escalation held in reserve if any residual stall:
  per-pair striped locks for truly-concurrent commits (WAL group-commit coalesces).
- **✅ Prod run 2026-07-07 (pprof build):** the NoSync write-stall did **not** recur —
  during the score phase pprof showed 0 mutex waiters, 0 goroutines parked on `s.mu`,
  48 workers runnable in compute, 0 swap; graceful cancel **worked** this time
  (`context canceled` propagated, worker pool drained, no restart, ~1m48s). BUT the
  run could not reach clean completion: it exposed a **separate O(N²)** in the
  scoring-path `CollectISBNASIN` (full `GetAllBooksCore` scan per ISBN-bearing book,
  ~50 books/min → ~16 h), previously *masked* by the write-stall. Caveat: because the
  O(N²) throttled candidate writes to a trickle, this run proved *mechanism + cancel*,
  **not** stall-cured-under-load — the real load test needs the fast pass below.
- **✅ O(N²) fix MERGED** (PR #1857 / commit `c36c05f4`): grafts the emission path's
  ISBN index onto `CollectISBNASIN` (O(matches)) + a `ctx.Err()` check on both paths.
- **✅ CLEAN COMPLETION PROD-CONFIRMED 2026-07-07 22:19** (op `01KWZQPTFYZY64AD1YB16A433D`,
  build `gc36c05f4-debug`): after `dedup.build-isbn-index` (7524/7524 indexed, 0 failed)
  set `IsISBNIndexBuilt()=true`, `dedup.full-scan` ran **end-to-end to 100%**
  (`44329/44329`, `duration_ms=675848` ≈ 11 min) — first clean completion since the
  incident. **Backlog cleared/rescored: 10869 pending candidates.** The score phase hit
  **606 books/sec** (was ~0.8/sec broken), and NoSync was finally load-tested under a
  *fast* write rate: `s.mu` stays a serialization point (waiters oscillate up to ~NumCPU,
  observed 44–46 in the write-heavy tail) but with the fsync gone each hold is microseconds,
  so the queue **drains rather than stalling** — **zero swap**, clean finish. Striped locks
  remain a live *throughput* optimization, never needed for correctness. pprof build then
  reverted to the normal prod build (`make deploy`, pprof off). **#19 CLOSED.**
- Downstream (separate, owner-gated): `dedup.calibrate-embedding-thresholds`
  (precision-target-not-met) can now be re-run against the freshly-scored 10869 backlog.

### Original incident notes (2026-07-06, pre-root-cause — kept for history)

- **Recurrence of the original incident**, post-fix. Triggered `dedup.full-scan` on
  prod (op `01KWTFW0T833JP6Y3PZCZK6YEG`) after shipping the `BookSignatureScan`
  memdb fix (PR #1830). Layer-1 (CONC-4) completed normally. The unified-scoring
  "Composing scores" pass (CONC-2) advanced to 21,407/48,623 (44%) then **produced
  zero further progress log lines for 9+ hours** — not just slow, genuinely frozen
  (confirmed via `journalctl`: only routine `/metrics` polling and scheduler ticks
  during the entire window, no scan-related log lines at all).
- **`DELETE /api/v1/operations/v2/:id` (graceful cancel) did not stop it** even
  after several minutes of waiting — same non-responsive behavior the original
  incident had, despite PR #1809's `ctx.Err()` check in
  `runUnifiedScoringForBook`'s per-candidate loop. Whatever is actually blocking
  isn't caught by that check — plausible causes: a lock held indefinitely
  (`de.mergeMu` from CONC-4, or another guard), or a blocking call to the local
  Ollama embedding/LLM backend (172.16.3.22) that doesn't respect context
  cancellation.
- **Service showed swap usage** (456M, peak 772M) at time of the stall — memory
  pressure may be a contributing factor, not just a pure logic hang. Worth
  correlating: does the stall coincide with a specific book having an unusually
  large pending-candidate set (the same shape as the original incident), or with
  memory exhaustion forcing the OS to swap and everything grinding to a halt?
- **Resolved by `systemctl restart`** (hard restart, not graceful cancel) — same
  remediation as the original incident. No data lost (per-candidate/embedding
  writes are durable; book_signature layer retained 649 pending, acoustid layer
  retained 211 pending across the restart).
- **Root cause NOT yet identified.** Needs: (1) find which book/candidate pair was
  in flight when it froze (no per-book log line was ever emitted for whichever
  book started around the 21,407 mark, unlike the smooth progression before it),
  (2) audit `runUnifiedScoringForBook` for any blocking call *after* the CONC-4/
  PR #1809 cancellation check that itself doesn't respect `ctx`, (3) check whether
  `de.mergeMu` (CONC-4) could deadlock under some interleaving, (4) correlate with
  the swap/memory pressure — a heap profile (`make deploy-debug` for pprof) next
  time would help nail this down live instead of just restarting.

- **Found during STOREFID P3-W3 caller audit.** Full write-up:
  [`docs/audits/2026-07-05-updatebookfile-memdb-writeback-fingerprint-wipe.md`](docs/audits/2026-07-05-updatebookfile-memdb-writeback-fingerprint-wipe.md).
  Independent of STOREFID — existed on `main`.
- **Root cause:** `PebbleStore.UpdateBookFile` was a blind full-record replace with
  no preserve-on-empty guard (unlike `UpsertBookFile` / `BatchUpsertBookFiles`). Jobs
  that read `GetAllBookFiles()` (memdb-slim → `AcoustIDFingerprint` + 3 diagnostic
  fields nil under prod `UseMemDB=true`) and write the struct back via bare
  `UpdateBookFile` wiped the stored fingerprint in Pebble.
- **PR-A (✅ done):** `AcoustIDFingerprint` preserve-on-empty guard on `UpdateBookFile`
  + two-direction regression test. Stops the critical wipe for all 4 writeback jobs
  (`recompute_itunes_paths`, `enrich_book_files` — DryRun false; `fix_book_file_paths`,
  `repair_missing_files` — DryRun true). Diagnostic fields NOT guarded here (backfill.go
  clears them on success via this method); the residual diagnostic wipe for failed-fp
  books is closed structurally by W3.
- **PR-B (✅ done):** rerouted the 3 HEAVY-READ fingerprint no-ops (`acoustid online_lookup`,
  `lsh_backfill`, `dedup lsh_index_build`) to proxy-then-hydrate (gate on
  `AcoustIDFingerprintDurationSec`, hydrate via `GetBookFiles`). `lsh_index_build`
  converted to a `RunItems` pool partitioned by BookID (index-map grouping to avoid
  doubling BookFile RSS). Restores LSH/online-lookup coverage that was silently a
  no-op under prod memdb. Race-safe (mock mutex; prod code was already safe).
  - **Follow-up (verify invariant):** all 3 ops gate on `AcoustIDFingerprintDurationSec > 0`
    as the memdb-safe "has whole-file fp" proxy, assuming `AcoustIDFingerprint set ⇒
    DurationSec > 0` (backfill.go writes both together). A historical row with a blob but
    `DurationSec == 0` would be silently skipped. Verify with a prod query; if any exist,
    backfill DurationSec or broaden the gate.
- **PR-C = STOREFID W3:** retype `GetAllBookFiles → []BookFileCore`; the 4 writeback jobs
  move to field-scoped update/hydrate. **W3 landmine:** those 4 must NOT use a
  `BookFileCore.ToBookFile()` bridge then write back (re-introduces the wipe).

## ✅ BookSignatureScan memdb no-op fix (2026-07-05)

- **Found while verifying the CONC-1..15 concurrency sweep's follow-ups.**
  `BookSignatureScan` filtered on `Book.BookSigV1`, which is stripped to nil
  by memdb (`stripBookForMemdb`) under `PebbleStore.UseMemDB=true` — the
  shipped production default. The scan silently matched zero books, no
  error surfaced.
- **Fix:** `Engine.getAllPrimaryBooksWithFullFields` sources from
  `GetAllBooksFrom` (already bypasses memdb per-book via `GetBookByID`)
  instead of `GetAllBooks`. Regression test proves the pre-fix shape (0
  candidates) and post-fix shape (correct candidates) both explicitly.
- **Checked, not affected:** `AcoustIDScan` — reads fingerprints via the
  per-book `GetBookFiles` call, which is unconditionally Pebble-direct.

## ✅ FullScan cancel-responsiveness fix (2026-07-05)

- **Incident:** cancelling a running `dedup.full-scan` op tonight took 90+
  seconds to take effect and eventually required a hard `systemctl restart`.
- **Root cause:** `runUnifiedScoringForBook`'s inner per-candidate loop
  (`internal/dedup/engine.go`) had no `ctx.Err()` check — only the outer
  FullScan per-book loop did. A book with an unusually large pending-candidate
  set could keep the scan running well past cancellation.
- **Fix shipped:** added a cancellation check at the top of the per-candidate
  loop, returning `ctx.Err()` promptly. Regression test
  `TestRunUnifiedScoringForBook_StopsPromptlyOnCancel`
  (`internal/dedup/engine_cancellation_test.go`) verified to fail without the
  fix and pass with it. No concurrency/worker-pool changes — that's separate,
  larger work still to be scoped.

## 🧾 Consultancy Evaluation — [`docs/consultancy/`](docs/consultancy/) (2026-07-02)

Full 6-dimension evaluation (101 findings, `file:line`-cited, top code findings
adversarially verified). Ranked roadmap: [`docs/consultancy/00-ROADMAP.md`](docs/consultancy/00-ROADMAP.md).

> **Task briefs exist for all of these (2026-07-03):**
> [`docs/agent-tasks/consultancy-roadmap/`](docs/agent-tasks/consultancy-roadmap/)
> — 31 briefs, 6 waves, model-tiered. **Wave 1 (14 tasks) SHIPPED 2026-07-03, PRs #1744–#1759.**
> **Wave 2 (8 tasks) SHIPPED 2026-07-03, PRs #1761–#1770** (T03 #1763, T13 #1768, T20 #1769,
> T21 #1764, T22 #1770, T24 #1762, T25 #1761, T29 #1766; aux #1765/#1767).
> **Wave 3 (4 tasks) SHIPPED 2026-07-03** (T10 #1775, T14 #1772, T15 #1774, T26 #1773;
> plus cr-03b recovery-apply #1776, silence-retry rescue #1688, flake fixes #1777–#1781).
> Next: wave 4. CONSULT-1..8 map to TASK-01..09 there. Run via `run.sh` + `orchestration.md`.
>
> **Owner-greenlight queue status (2026-07-03):**
> - ✅ `dedup.drain-stale` (#1768) — dry-run AND apply DONE on prod: 12,531 inspected, 3,076 reclassified, 9,455 kept
> - ✅ Recovery audit dry-run DONE: 0 BookSigV1 wipes; 397 descriptions recoverable; apply mode shipped #1776, restore run post-deploy
> - ⏳ `dedup.calibrate-embedding-thresholds` (#1774) — run after re-embed completes, review, set `embedding_thresholds_by_model`, then full-scan (owner-gated).
>   **2026-07-04:** post-orphan-cleanup + gold-label-rebuild run came back
>   `high=target-not-met low=target-not-met` (1,661 pairs scored, 953 true_dup /
>   708 not_dup) — no cut-point in `[0.80, 0.99]` reaches even the 90% low
>   target. Added observability (best-achieved-precision + highest-cosine
>   not_dup sample, report-only, no code changed to targets/math) to
>   distinguish mislabeled gold vs. a genuine bge-m3 cosine ceiling — see
>   CHANGELOG. Next: re-run the op and read the new diagnostic fields/sample
>   to make that call (owner-gated, not done as part of this change).
> - ⏳ NutsDB retirement PR 2 (file/dep removal) — after prod soak of #1770's Pebble-only cutover
> - 📋 29,083 books never had a Description — needs a metadata-fetch campaign (Audible / transcription metadata cache), separate from recovery

Tier-0 items (high impact, low effort — do first):

- [x] **CONSULT-1** ✅ #1749 — EmbeddingScorer store fast-path: model/dimension check + F1 fallback on degenerate zero-score results (MATCH-1/BUG-1 — live during bge-m3 re-embed) — `internal/ai/embedding_scorer.go:92-98`
- [x] **CONSULT-2** ✅ #1747 (guard; recovery audit = TASK-03, wave 2) — memdb preserve guard on `UpdateBook` (mirror PERF-7 BookFile guard); recover wiped Description/BookSigV1 from `book_ver:` snapshots BEFORE any pruning (STOR-1/QUAL-2) — `internal/database/pebble_store.go:2664`
- [x] **CONSULT-3** ✅ #1744 — Model-aware re-embed skip in `EmbedAuthor` + `EmbedBooksAsync` (DEDUPC-1/TOGGLE-2) — `internal/dedup/engine.go:2243,2306`
- [x] **CONSULT-4** ✅ #1751 — Unschedule/gate nightly `dedup.embed-async` (quota-dead OpenAI Batch API) (OPS-3) — `internal/plugins/dedup/embed_async.go:24`
- [x] **CONSULT-5** ✅ #1745 — Drop OpenAIAPIKey requirement for keyless local backends (TOGGLE-1) — `internal/ai/register.go:35`
- [x] **CONSULT-6** ✅ #1750 — Pre-commit hook doesn't actually block `.claude/.credentials/`; SHA-pin `security.yml` (SEC-2/SEC-5) — `scripts/setup-git-hooks.sh:17-27`
- [x] **CONSULT-7** ✅ #1748 + #1758 — Commit deploy recipe templates + `scripts/manage-ollama-windows.py`; add rollback target (OPS-1/OPS-6)
- [x] **CONSULT-8** ✅ #1757 — API-key rotation/expiry for bootstrap-issued keys (SEC-1 — previously untracked anywhere)

Tier-1+ (backend-mode toggle, 384K stale-candidate drain, bge-m3 threshold
recalibration, fingerprint campaign, dedup auto-resolve op, slog-corruption
sweep, shutdown escape-hatch, HNSW staleness, monitoring): see the roadmap.

## 🤖 Agent Task Package — [`docs/agent-tasks/`](docs/agent-tasks/) (refreshed July 1, 2026)

Hand-run, weak-model-proof task briefs (worktree-disciplined, portable subagent
roster, multi-agent orchestration scripts). Use these instead of the unreliable
TODO→GitHub-issues bot. Planning + cost/efficiency rationale (buckets, per-task
model tier, same-file collision→wave table) is in
[`docs/agent-tasks/BREAKDOWN-2026-07-01.md`](docs/agent-tasks/BREAKDOWN-2026-07-01.md).

**Active workstreams (8 · 30 briefs):**

- 🔵 **dedup-hardening/** (P1) — 3 tasks: `upsertExactCandidate` boilerplate/min-duration guard (DEDUP-INTRO-1 residual), CONS-15 part-vs-whole guard, CONS-FRAG-2 multi-file organize.
- 🔵 **ci-flaky-fixes/** (P1) — 3 tasks: mockery pin/regen (Mock Freshness), `TestBackupEndpointsErrors`, `TestScanService_MultiChapterAudiobook`.
- 🔵 **library-ui/** (P2) — 4 tasks: EMB-UI-1 Ollama link, USER-QUICK-FILTERS saved presets, TAG-SEARCH filter/cloud, Library stale-cache bugfix.
- 🔵 **dedup-dataset/** (P2) — 5 tasks: C5-sig, C5-folder, C5 live-capture, C7 JSONL export, C8 auto-bug-filing (deferred on backfill).
- 🔵 **provenance-hash-chain/** (P2) — 2 tasks: HASH-CHAIN-1 download-hash field, HASH-CHAIN-3 integrity alert.
- 🔵 **perf-cleanup/** (P3) — 5 tasks: ARCH-4b reset_all RunItems, MAYDEPLOY-H5/H7 fast-paths, NUTSDB tidy (optional), CONS-13 shim retire (gated).
- 🔵 **logging-slog/** (P3) — 3 tasks: SLOG-W13 residual (writeback+ISBN, iTunes sync, scanner deep paths).
- ⚪ **ai-responses-migration/** (P3, **DEFERRED/optional**) — 5 tasks: AI-RESP-A/B/D/E/F Chat Completions → `/v1/responses`. Do not start without greenlight.

**Not turned into briefs** (see BREAKDOWN doc): design/brainstorm-first items
(WF-0..6, 3.8 Plex, 4.1 PG, 3.9/3.10, 1.17 rename, REPO-SIZE-1, CONS-17b) and
operational/prod-verification items (CONS-10, PH-2, PD-3, I1/I6, SEC-AUDIT-11,
SLOG-PROD-VERIFY, DEDUP-CANDIDATE-EXPLOSION).

**✅ Archived (shipped) → [`docs/archive/agent-tasks/`](docs/archive/agent-tasks/):**
`transcription-matching/` (5/5), `dedup-intro-falsepositive/` (4/4),
`dedup-ui/` (5/5), `system-docs/` (→ `docs/system/`) — all verified complete 2026-07-01.

### ✅ Shipped June 30, 2026 (PR #1688)

- 🟢 **Silence retry loop** — 0-char Whisper results retried with 300s same-file clip, then 90s of second audio file; exhausted books marked `[SILENCE]` and skipped on future sweeps (`retry_silence=true` to re-include).

### ✅ Shipped June 28, 2026 (PRs #1660, #1661)

- 🟢 **Library pagination** — page-2-returns-0 (double-pagination) FIXED; "500 means 500" honored; quarantine pushed into the indexed scan; cap 500→1000. Verified on prod.
- 🟢 **Transcription discovery wiring** — `hintsFromBook`/`transcriptionBoost` in fetch + scoring.
- 🟢 **Whisper intro parser** — staged extractor (publisher prefix, read-by narrator, noise truncation); `reparse_only` op corrected existing data on prod.

### 🐛 Known flaky CI (pre-existing, capture-and-fix later)

- 🟢 **Mock Freshness** ✅ FIXED (#1718, 2026-07-01) — pinned mockery to v3.7.1 (CI + Makefile + setup script); v2 could not generate the merged-file `.mockery.yaml`. `make mocks-check` green.
- 🟢 `TestBackupEndpointsErrors` ✅ FIXED (#1711 — dead `os.Chdir` race removed, 20/20) · `TestScanService_MultiChapterAudiobook` ✅ FIXED (#1713 — missing `WaitForWarmup` in `SetupIntegration`, 20/20). NOTE: a *separate* pre-existing `pebble: closed` shutdown race remains under package-wide `-race` — see `PEBBLE-CLOSED-SHUTDOWN-RACE` in Open Bugs.

### 🐛 Known bug: Library page's client-side cache is never invalidated on mutation (2026-07-01) — ✅ FIXED #1719

- 🟢 ✅ **FIXED (#1719, 2026-07-01):** all ~13 remaining mutation handlers in `Library.tsx` now call `clearLibraryCache()` before `loadAudiobooks()` (+ regression test). Original description retained below for context.
- 🔴 `web/src/stores/useLibraryCache.ts` (60s TTL) is read by `useLibraryQuery.loadAudiobooks` before every fetch and is served as-is on a cache hit. Nothing ever invalidates a specific entry or the whole store on mutation — only `handleMergeAsVersions` and `handleCombineIntoOneBook` in `Library.tsx` were fixed (PR merging this note) to call the new `clearLibraryCache()` before reloading. At least ~14 other mutation handlers in `Library.tsx` (`handlePurgeOne`, `handleRestoreOne`, `handleConfirmPurge`, `handleBatchRestore`, `handleVersionUpdate`, `handleFetchMetadata`, `handleParseWithAI`, the batch-delete/apply-metadata handlers, etc. — grep `loadAudiobooks()` call sites) call `loadAudiobooks()` right after a mutation without clearing the cache, so they have the same latent bug: a cached page can serve stale rows (deleted/restored/edited books, wrong order) until the 60s TTL lapses. Fix: either call `clearLibraryCache()` in each remaining handler, or thread a `bypassCache` flag through `loadAudiobooks` and default all mutation call sites to bypass.

---

## 🎯 Current Status — June 26, 2026

**Library:** ~31,400 books / 8,837 authors / 21,668 series
**Production:** PebbleDB primary; Linux, HTTPS at prod server; stable
**Latest activity:** Batch Whisper bulk transcription RUNNING on prod (op `01KW2PHQ1M0NPNDPAMVZ7ZW8M9`).
GPU confirmed active (device=cuda, GTX 1050 Ti). ~13 min/page × ~55 pages (200 books/page, 10,891 total).
Dep pins confirmed (numpy<2, setuptools<67, --index-strategy unsafe-best-match — PR #1637).
Crash-recovery checkpoint added — cursor persisted after each page (PR #1638).
First page: 199 books transcribed. Run auto-resumed after deploy restart.
**In flight:**
- `maintenance.transcribe-book-intros` — bulk run in progress; ~55 pages; ~12h total; auto-resumes on restart
- After bulk run completes: re-trigger `maintenance.itunes-heal` to resolve 292 ambiguous tracks using Layer 6 (Whisper transcription-based disambiguation)

**iTunes path heal remaining work:**
- 3,720 ambiguous — disambiguation tied; improve scoring or use DB series info for the
  208 unique book IDs in the ambiguous set.
- 5,349 not found on disk — files not present in organized library or newbooks; may need
  newbooks track-number-aware matching (e.g. `...League of Losers, Book 1 - 032.mp3`
  vs iTunes expected `32 A Cat and His Human_ League of Lo.mp3`).
- 4,734 doubled-path records — separate issue: base path embedded twice in `FilePath`;
  files exist at the second path occurrence. Not yet addressed.

---

## 🛠️ Pipeline Hardening — remaining (2026-06-21)

> Full audit + prioritized fixes: [`docs/dedup-import-pipeline-audit.md`](docs/dedup-import-pipeline-audit.md).
> Session handoff: `.remember/remember.md`.

- [x] **PH-1** ✅ Tag-backfill completed 314,893/314,893 (op `01KVQVAZK0TH0FRFPNMMBYJKJQ`, 2026-06-22). Lossless-library goal done.
- [ ] **PH-2 (P2)** Differentiated residual-triage op (PR #1619 merged, deployed).  <!-- 2026-07-01: ⏳ code shipped (maintenance.dedup-exact-triage op); remaining is a prod run + population review. -->
      `maintenance.dedup-exact-triage` shipped: classifies all pending candidates into
      genuine/stub/fragment/title_leak/unknown. **Next: enqueue on prod and review the
      population breakdown. Purge wave (PH-2b) is a separate PR — never blanket-purge.**
- [x] **PH-3 (P1/P2)** ✅ Dedup perf shipped in PR #1617: O(N²)→O(1) embedding map, hoisted book-only collectors, purge cap 100K→1M.
- [x] **PH-4** ✅ Decision: LEAVE the `strings.Contains(normTitle(parent), normTitle(prefix))` guard as-is. Weakening the string test risks silent mis-merges across tens of thousands of iTunes books for only 5 edge cases. These 5 folders can be fixed manually via AP-1 "Combine into one book". No code change needed.
- [x] **PH-5** ✅ `UpsertBookFile` preserve-on-empty guard already added in PR #1587 (PERF-7). AcoustIDFingerprint, FingerprintFailureReason/Detail/DiagnosticJSON all preserved on nil/empty incoming fields.

---

## 🚧 Active Projects — in flight (2026-06-21, this session)

> Live working set so nothing is lost between sessions. Status flags:
> 🟢 done · 🟡 in progress / branch exists · 🔵 designed, not started · 🔴 blocked.

### AP-1 ✅ Generic "Combine into one book" merge
All components confirmed in main: `merge.Service.CombineBooks`, `POST /audiobooks/combine` route,
`duplicates.MergeService.CombineBooks` iface, `api.combineBooks` frontend function, Library toolbar
button + dialog (LibraryToolbar.tsx → BatchToolbar.tsx → LibraryDialogs.tsx).
- 🔵 Follow-up (AP-1b): when survivor's files are under RootDir, physically move them into one
  folder (user wants co-location only inside the library; leave abooks/ etc. in place).

### AP-2 ✅ Persistent metadata-review undo
Confirmed in main: `POST /audiobooks/:id/undo-last-apply` backend (`wire_audiobooks_routes.go:58`,
`handler_metadata.go:159`), `api.undoLastApply`, and persistent Undo button in
`BulkMetadataSearchDialog.tsx:731` ("remains usable after the banner disappears").

### AP-3 ✅ Duration extraction fixed — real durations backfill ~complete
Fixed in PR #1555 (`internal/mediainfo`: calls ffprobe first, estimate only as flagged fallback).
Both import paths now correct: filesystem scan uses `BuildFromTag`→`realDurationSec`; iTunes import
uses `track.TotalTime/1000`. Backfill ops:
- `maintenance.duration-backfill` — corrects iTunes ms→s inflated rows (CONS-16). Completed (17,684
  files / 1,210 books corrected).
- `maintenance.duration-reextract` v3 (Jun 21) — fingerprint-first; 6,540 corrections written
  (apply run, PRs #1562–#1565). **Watchdog false-cancel fixed** (PRs #1566–#1567: atomic clock +
  sdk.PageBooks keepalive + heartbeat-at-top). Dry-run (Jun 22) confirmed `would-change=0` across
  ~30K books; **~721 tail books still pending** (new imports + apply-cancel tail). Re-enqueue apply
  once dry-run confirms final count.
- 🔵 ARCH (AP-3b): consolidate the 3 duration paths — `internal/mediainfo`, `internal/diagnosis/probe`,
  external (Audible) — into ONE accurate extractor. Lower priority now that the extractor is fixed.

### AP-4 ✅ tag-backfill apply (lossless RawTags) — COMPLETE
Completed 314,893/314,893 at 2026-06-22T14:38 (op `01KVQVAZK0TH0FRFPNMMBYJKJQ`). All RawTags
backfilled. Lossless-library goal achieved.

### AP-5 ✅ Same-folder untagged track shattering (import root cause #2)
Fixed in PR #1618: `DetectMultiFileGroup` now groups files when ALL tags are absent
(universally empty album + album_artist). Sequential filenames alone are sufficient evidence
for untagged tracks. Scanner uses folder name as book title for no-tag groups.

---

## 🔀 Consolidated Session Work Queue (2026-06-19)

> Three parallel Claude sessions (`refactor-score-badge-chip`, `fix-scanner-part-book-dedup`,
> `dedup-ux`) were merged into one to stop conflicts. Full handoff:
> `~/repos/temp/session-status-CONSOLIDATED-HANDOFF.md`. Ordered burn-down list:

- [x] **CONS-1** ✅ **PR #1515** merged (`feat/path-abbrev`, Track A — path abbreviation `$(libroot)`/`$(books)`).
- [x] **CONS-2** ✅ **Track B** merged — 3-way `LabelToggle` (Dup/Unsure/Not), clickable rows, abbreviated paths in `DedupLabels.tsx`.
- [x] **CONS-3** ✅ Stale fix branches verified merged.
- [x] **CONS-4** **BookDedup.tsx row redesign** (dedup-ux task, 0 code written) — apply `renderBookCard` pattern (cover tall-left `alignSelf:'stretch'` w56 h100%, quality chip inline after title, larger title, remove bottom chip whitespace) to `renderBookSide` (~line 1057) in the 2907-line `web/src/pages/BookDedup.tsx`. — ✅ verified done 2026-07-01: BookDedup.tsx now a 145-line shim → UnifiedDedupTab.renderBookCard
- [x] **CONS-5** = **DEDUP-FOLDER-1** — ✅ `FolderFilesChip` (lazy popover w/ file list, count, format/size/duration) wired into `UnifiedDedupTab` cards.
- [x] **CONS-6** **Track C** — metadata-compare tab in `CandidateCompareDrawer.tsx` (alongside Fingerprint tab): series/narrator/parts/duration/size/which-signal-fired, from `GET /api/v1/dedup/candidates/:id/breakdown`. — ✅ verified done 2026-07-01: metadata-compare panel in CandidateCompareDrawer.tsx (metadata-compare-panel)
- [x] **CONS-7** = **DEDUP-KB-1** (see Dedup UX section) — keyboard shortcuts. — ✅ verified done 2026-07-01: = DEDUP-KB-1 (done)
- [x] **CONS-8** **Track D2** — ✅ code complete. Real root cause was iTunes import, NOT the scanner: `groupTracksByAlbum` empty-album fallback keyed per-chapter track name. Added `titleutil.StripChapterSuffix` to collapse trailing part markers (`Title – 11/23`) so chapter parts group into one book; `Book.Title` cleaned too. (`scanner.go groupFilesIntoBooks` was a red herring — only the FS walk uses it and its multi-file detection is already correct.)
- [x] **CONS-9** ✅ **Track D1** — `dedup.quarantine-chapter-artifacts` now catches unscanned (Duration=0) idents via `MinTitleCollisionsUnscanned` gate (merged #1511). Prod dry-run finds only ~53 short idents — confirms most of the 380K candidates are NOT chapter artifacts but real books with corrupt metadata (see CONS-16/17). **DRY-RUN ONLY; show list before apply.**
- [ ] **CONS-10** **Track D3** — drain stale exact candidates. **Code fixes CONS-16 + CONS-17 now MERGED**; remaining blocker is the **backfill/re-scan** on prod (run `maintenance.duration-backfill` dry-run → apply; re-scan affected filesystem books so corrected titles land) so candidates self-resolve, THEN re-scope. ⚠️ Prod investigation (candidate 211) proved the 380K exact candidates are a MIX: some real dups + many real multi-file books mislabeled by the duration-ms + title-leak bugs. Quarantining before backfill = **DATA LOSS**. **Re-run `dedup.quarantine-chapter-artifacts` dry-run AFTER backfill, show the list, get explicit user OK before any apply.**  <!-- 2026-07-01: ⏳ code fixes (CONS-16/17) merged; blocked on prod duration-backfill + re-scan (DATA-LOSS gate — dry-run + user OK). -->
- [x] **CONS-11** **Track D4** — manual-import UI button (op `library.import` already merged/deployed; no frontend yet). Add a button/dialog on the Library page that POSTs `{def_id:"library.import", params:{path}}` to `/api/v1/operations/v2` and polls. ~Lowest-effort remaining UI task. — ✅ verified done 2026-07-01: Library.tsx manualImport dialog + api.startLibraryImport
- [x] **CONS-12** = **DEDUP-INTRO-1** (see Dedup UX section) — Audible intro/outro false-positive dedup candidates. — ✅ verified done 2026-07-01: = DEDUP-INTRO-1 (done)
- [ ] **CONS-13** = **CFG-2 Phase D** — retire flat-key compat shim in `internal/server/update_service.go` (low priority; after 1+ wk prod stability).

### Metadata-repair track (root causes of the dedup candidate explosion) — investigated 2026-06-19

> Read-only investigation complete. Root-cause map below. **No code written yet.** A design spec is the
> next step (CONS-18 needs brainstorming/approval before implementation; CONS-16/17 are clear bug fixes).
> The user's framing: "after we eliminate the nonsense in our own repo, we should probably have some sort
> of normalization filter that runs on import that handles that and writes the correct time back to the
> primary file."

- [x] **CONS-15** **(D3-emitter)** part-vs-whole guard in the exact dedup emitter — defense-in-depth so a part can't pair 100% against a whole even if metadata is wrong. **Deprioritized** (fixing CONS-16/17 removes most of the cause).  ✅ shipped #1712 (agent-task sweep 2026-07-01)
- [x] **CONS-16** ✅ **Duration-unit bug** — FIXED. (a) Extracted `trackDurationSeconds()` in `internal/itunes/service/importer.go`; routed all 3 write sites (now lines ~311/655/703) through it with `/1000`; added `trackDurationSeconds` unit test + a seconds assertion in the integration test (which previously mirrored the bug). (b) New dry-run-gated maintenance op `maintenance.duration-backfill` heals existing inflated rows. **Detection changed from the planned filesize/bitrate formula** — the iTunes importer never populates `BitrateKbps` (the `itunes.Track` struct has no BitRate field), so those rows have `BitrateKbps=0`. Replaced with an **implied-bitrate** test (millis if duration-as-seconds implies <4 kbps, with a 3 Mbps upper sanity bound) that needs only `FileSize` and never flags genuine low-bitrate audiobooks (advisor-reviewed: a 16 kbps floor would have corrupted 12 kbps spoken-word books). Per book: corrects each file then re-runs `RecomputeBookAggregates`. Dry-run default — no prod data touched until run with `dryRun=false`. Shipped in PR (branch `fix/cons16-duration-units`).
- [x] **CONS-17** ✅ **Multi-file title leak** — FIXED, both paths. (1) **iTunes** `buildBookFromAlbumGroup`: empty album on a multi-file group now derives the title from the common parent **folder** before falling to the per-chapter track Name (scoped to multi-file; single-file keeps stripped track Name). (2) **Filesystem scanner**: sequential multi-file groups (`SegmentFiles>1`, `FilePath=segs[0]`) are now routed through `AssembleBookMetadata` via a condition change at `scanner.go:670` (`IsGenericPartFilename(filePath) || len(SegmentFiles)>1`) — NOT by setting `FilePath=dir` (which would drop the detected `SegmentFiles` subset and rescan the whole dir). Segments still created at the saveBook step. Tests: `TestBuildBookFromAlbumGroup_EmptyAlbumUsesFolder`, `TestAssembleBookMetadata_GenericChapterUsesFolder`. Shipped in PR (branch `fix/cons17-title-leak`).
- [ ] **CONS-17b** **(follow-up, residual)** Multi-file group whose first chapter has a *non-generic* tag title (e.g. "Big Finish Ident") still prefers that tag over the folder, because `resolveTitle` (`assemble.go:88`) trusts non-generic tag titles. Robust fix needs a **"do all chapters agree on their `tag.Title`?"** discriminator (agree → it's the book title; disagree → fall to folder). ⚠️ Album-preference was **rejected**: album frequently equals the *series* name (`assemble.go:139` already notes this), so preferring album would replace correct titles with series names. Needs a small design before code. **Partially done (CONS-FRAG):** the iTunes importer path now implements exactly this all-chapters-agree discriminator (`agreedStrippedTitle`); the residual is the **filesystem scanner** `resolveTitle` path.
- [x] **CONS-FRAG** ✅ **iTunes book fragmentation** — FIXED. `groupTracksByAlbum` keyed `artist+"|"+album`, fragmenting (1) multi-author anthologies (constant album, per-story Artist → one book per author, e.g. "Wild Cards I") and (2) empty-album chapter files whose " - Part NN" suffix wouldn't strip ("Aces Abroad - Part 19"). Verified against prod via `/external-ids` (both iTunes-PID-linked) + ffprobe. Now keys on **album alone** when present, `name:<artist>|<stripped>` when empty, with a track-number-repeat **over-merge guard** (`splitOverMergedGroup`) protecting series-as-album. `titleutil.StripChapterSuffix` strips bare `- Part NN`/`- Chapter N`/`- CD N` (excludes `Book N`/`Volume N`). CONS-17b agree-title discriminator applied to the iTunes path. Forward-only; tests in `grouping_test.go` + `strip_test.go`. ⚠️ Existing fragmented+organized books need a **separate dry-run-gated re-group op** (un-organize = destructive; surface before applying — see CONS-10).
- [x] **CONS-FRAG-HEAL** ✅ **built + dry-run → library already correctly grouped (no mass heal needed).** Dry-run-gated `maintenance.itunes-regroup` op (PRs #1542–#1546): in-place re-group via per-PID `ReassignExternalID` + `MoveBookFilesToBook` (NOT delete+reimport — canary proved purge tombstones PIDs; `.claude/notes/itunes-heal-canary-findings.md`); frozen deterministic exclusive-claim plan. **Prod dry-run: consolidate=0, fresh=0 → ZERO fragmentation/over-merge remain.** Completeness: complete-groups≈11,000, partial≈712, single-file-in-album=554. **Duration-backfill applied (17,684 files / 1,210 books ms→s)** then duration-bucketed the 554: <15min=7 (anthology pieces, correct), 15-90min=181 (short books), ≥90min=366 (complete books, false alarm). ⇒ **No orphaned-chapter problem; grouping healthy.** `dryRun:false` would change ~nothing (only 173 entangled groups, skipped). Residual (separate, NOT auto-run as unsafe/unclear): 173 entangled-would-move (task: manual/v2); 10,374 unresolved PIDs = import gap (benign, complete books present); **~383,902 stale dedup candidates** computed pre-fix — next workstream is dedup re-detection/purge to clear the original 6/47 false-match backlog.
- [x] **CONS-FRAG-2** **(follow-up)** A newly-merged multi-file iTunes book whose chapter files are scattered across folders gets `Book.FilePath` = their common parent dir. `organizeOneBook` calls only the single-file `OrganizeBook`, which **safely refuses a directory `FilePath`** (early return at `organizer.go:98`, no file move) — so the book stays `imported` instead of organizing. Non-destructive but incomplete: route multi-file books (BookFiles>1) to `OrganizeBookDirectory(book, segmentPaths)` in `organizeOneBook`. BookFiles already carry correct per-track paths.  ✅ shipped #1709 (agent-task sweep 2026-07-01)
- [~] **CONS-18** **Import-time duration-normalization filter** — **Part 1 (DB-side gate) DONE.** Shared predicate `database.DurationLooksLikeMillis` (implied-bitrate, promoted from the backfill op) + `normalizeBookFileDuration` repair wired into `CreateBookFile` / `UpsertBookFile` / `BatchUpsertBookFiles` so no ingest path can re-introduce ms durations; idempotent, FileSize-less rows untouched; full DB test suite green. Spec: `docs/superpowers/specs/2026-06-19-import-duration-normalization-design.md`. Shipped in PR (branch `feat/cons18-duration-gate`). **Part 2 (file-tag duration writeback) REMAINING** — user approved emitting a duration tag to the primary file. Scoping found it is non-trivial + low-payoff: per-file duration must be threaded through `BuildFullTagMap` (currently book-level), it only maps to a real frame for MP3 (`LENGTH`/TLEN, ms; MP4/M4B store duration in the container, no tag), `FilterUnchangedTags` needs a LENGTH case, and players read the VBR/container header not TLEN. Config-gate it + `isProtectedPath`-guard. Build after the dedup re-scope settles.

---

## 🖥️ Dedup UX — Open Items

### Keyboard shortcuts (future)
- [x] **DEDUP-KB-1** Add keyboard shortcuts to dedup page for power users: `j`/`k` to navigate rows, `m` to merge, `d` to dismiss, `s` to select/deselect, `enter` to open compare drawer, `esc` to close drawer, `shift+a` select all on page. Goal: make it possible to clear a page of dedups without touching the mouse. Design: global `keydown` listener active when no input/dialog is focused; shortcuts displayed in a `?` help overlay. — ✅ verified done 2026-07-01: UnifiedDedupTab.tsx DEDUP_SHORTCUTS + ? help overlay + shift-select

### Audible intro/outro false positives (backend investigation needed)
- [x] **DEDUP-INTRO-1** Investigate why Audible intro/outro clips ("Audible Opening Message", "Introduction") generate exact-match dedup candidates across unrelated books. Root cause: the exact layer fingerprints individual audio files and short intro clips have identical chromaprints across all books from the same publisher. Fix candidates: (a) filter candidates where both titles are known Audible boilerplate strings (title blocklist); (b) skip fingerprint comparison on files shorter than a configurable threshold (e.g. <60s); (c) check at the book level first — if books have different ISBNs/ASINs, don't flag file-level matches. Track prod volume: ~372K total candidates, many likely this class. — ✅ verified done 2026-07-01: boilerplateTitlePatterns suppress intro/outro fp seeding (engine.go:34); residual exact-title/duration leak tracked in dedup eval (upsertExactCandidate guard)

### Folder chip + file hover list
- [x] **DEDUP-FOLDER-1** Add a "folder" chip to dedup candidate cards when the matched path is a directory (multi-file book). On hover/click, show a popover listing all files in that folder with their format, bitrate, size, duration. Helps distinguish "this is a 197-file m4b series" from "this is a single file" at a glance without opening the compare drawer. — ✅ verified done 2026-07-01: FolderFilesChip.tsx wired into UnifiedDedupTab (= CONS-5)

---

## 🏗️ Config Struct-Nesting Refactor (CFG-0 → CFG-1 done, CFG-2 settings-UI open)

> **CFG-0** (viper wiring fix) shipped PR #1464. **CFG-1** (backend struct nesting) Waves 1–8 complete (PRs #1468–#1484). All 77 flat fields nested.
> **CFG-2** (Settings UI reorg — the frontend "second half") **SHIPPED PR #1514** (Phases A/B/C/E); Phase D (retire flat shim) still open.
> API shape documented in `docs/reference/config-api-shape.md`.
> Pattern: define sub-struct → add field to Config → remove flat fields → migrate blob → add applySetting cases → add remapKeys shim → update callsites.

- [x] **CFG-0** Viper wiring: 21 embedding/dedup/metadata fields were bypassing the blob on first save. Fixed PR #1464.
- [x] **CFG-1 Wave 1** `EmbeddingConfig` — 5 flat embedding fields nested into `Config.Embedding`. PR #1468.
- [x] **CFG-1 Wave 2** `DedupConfig` — 9 flat dedup fields + 4 signal band thresholds nested into `Config.Dedup`. `SetBandThresholds` injection avoids `unified→config` circular import. PR #1476.
- [x] **CFG-1 Wave 3** `MetadataScoringConfig` — 7 metadata scoring fields nested into `Config.MetadataScoring`. PR #1479.
- [x] **CFG-1 Wave 4** `ITunesConfig` — 10 iTunes sync fields nested into `Config.ITunes`. PR #1480.
- [x] **CFG-1 Wave 5** `MaintenanceConfig` — 18 maintenance fields nested into `Config.Maintenance`. PR #1481.
- [x] **CFG-1 Wave 6** `ScheduledTasksConfig` — 23 scheduled-task fields (8 task groups) nested into `Config.Scheduled`. PR #1482.
- [x] **CFG-1 Wave 7** `AutoUpdateConfig` — 5 auto-update fields nested into `Config.AutoUpdate`. PR #1483.
- [x] **CFG-1 Wave 8** `GetConfig` secret-masking fix — all 5 secrets now masked via `MaskSecrets`; 2 new tests. PR #1484.

### CFG-2 — Settings UI reorganization (the "second half") — SHIPPED PR #1514

> **Done (2026-06-19):** Settings.tsx 3,077 → 1,395 lines. Dedup has its own tab (index 3). 11 new
> TypeScript interfaces. `loadConfig` reads nested keys first, flat fallback for compat.
> `handleSave` sends both nested + flat. 9 section components + `useSettingsHandlers` hook.
> 280 Vitest tests pass. `sanitizeImportPayload` fixed for nested objects.

- [x] **CFG-2 Phase A** — 11 nested TS interfaces added to `api.ts`. PR #1514.
- [x] **CFG-2 Phase B** — `loadConfig`/`handleSave` wired to nested keys; Dedup tab at index 3. PR #1514.
- [x] **CFG-2 Phase C** — Settings.tsx 3,077 → 1,395 lines; 9 section components + `useSettingsHandlers`. PR #1514.
- [ ] **CFG-2 Phase D — Retire the flat-key compat shim.** Remove flat→nested remap in `update_service.go`; keep blob migration. Gate on Phase B+C proven stable. (open — future PR)
- [x] **CFG-2 Phase E** — 5 Vitest unit tests + 1 E2E spec. 280 tests pass. PR #1514.

---

## 🚀 Embeddings + Vector Index — Activation & Follow-ups

> Captured 2026-06-15. Both features shipped & deployed default-off:
> local embeddings (PR #1452, Ollama/bge-m3) + HNSW vector-index backend (PR #1453).
> See `~/.claude/.../memory/project_embeddings_and_vector_index.md` and CHANGELOG (June 14, 2026).

- [x] **EMB-5** ✅ 2026-07-05 — Non-primary version-group members now get a real
  embedding computed/cached (calibration/QA datapoint) instead of having their
  row deleted and generation skipped. Fixes the `skipped_missing` count in
  `dedup.calibrate-embedding-thresholds` (110 as of the July 4 run) and
  unscorable gold-label pairs referencing non-primary books. Candidate
  generation stays primary-only (`findSimilarBooks` gated on
  `isNonPrimaryVersion` in both `CheckBook` and `FullScan`); also closed a
  related gap where the SQLite-fallback similarity path could surface a
  non-primary book as the *other* side of a primary book's match (chromem's
  ANN path already filtered this, the fallback didn't) —
  `internal/dedup/engine.go` (`prepBookEmbed`, `CheckBook`, `FullScan`,
  `findSimilarBooks`, new `getAllBooksUnfiltered`). See CHANGELOG.

**Done**
- [x] **VEC-1** HNSW backend flipped live on prod — `vector_index_backend=hnsw`, restarted 2026-06-15 (hydrates existing 3072-dim OpenAI vectors). Reversible: set back to `chromem` + restart.

**Activation — user-gated (dry-run, then checkpoint)**
- [x] **EMB-1** Code path: `EmbeddingClient.SetOllamaAvailable` gates `EmbedBatch` on local Ollama reachability. Prod activation (PUT config + restart) is user-gated.
- [x] **EMB-2** Code path: `reembed-embeddings` op checks `toolRegistry.Available("ollama")` before starting. Prod dry-run + apply is user-gated.
- [x] **EMB-3** Code path complete. Layer-2 enable (`dedup_embeddings_enabled:true`) after re-embed is user-gated.

**Infra / hardening**
- [x] **OLLAMA-1** Superseded by TOOL-4 (`OllamaDaemon`): `EnsureRunningOrAdopt` is called at `server.Start()`, so audiobook-organizer's own systemd unit provides the reboot durability — no separate Ollama unit needed.
- [x] **VEC-2** HNSW on-disk persistence via `Graph.Export`/`Import` — `.bin` + `.meta.json` snapshots per entity type; load at boot, save at shutdown. Shipped PR #1465.
- [x] **EMB-4** Deleted dead legacy `embeddings.db` (~1.8 GB) from prod on 2026-06-15.

**UI**
- [x] **EMB-UI-1** Add a "Download latest Ollama" link above the embeddings settings on the Settings page (deep-link to https://ollama.com/download), so an operator configuring a local backend can grab the binary without leaving the page. *(May be superseded by TOOL-1 managed auto-download.)*  ✅ shipped #1714 (agent-task sweep 2026-07-01)

---

## 🧰 Managed External-Tool Lifecycle (Ollama + fpcalc) — Captured 2026-06-15

> Full design + risks: [`docs/research/2026-06-15-tool-lifecycle-and-workflow-system.md`](docs/research/2026-06-15-tool-lifecycle-and-workflow-system.md)
> **Verified:** embeddings ARE cached (`emb:c:<model>:<textHash>` in Pebble) → Ollama only needs to run to generate NEW embeddings; steady state = mostly cache hits → can be down almost always. fpcalc already shells out (`exec.LookPath` + ffmpeg fallback + `ErrNotAvailable` graceful disable) — generalize that pattern. Static binaries exist for both (Ollama `.tar.zst`; Chromaprint fpcalc fully-static Linux on GitHub releases).

- [x] **TOOL-1** `ToolRegistry` (managed/system/custom/disabled resolution + cache), pinned multi-version manifest (`KnownTools`), `Resolve`/`Available`/`AllStatuses`/`InvalidateCache`. Shipped PR #1465.
- [x] **TOOL-2** SHA256-verified atomic `Downloader` + `POST /api/v1/tools/:name/install` endpoint; ollama registered with pinned manifest entry. Shipped PR #1465.
- [x] **TOOL-3** fpcalc registered in `KnownTools`; `fingerprint.SetResolvedFpcalcPath` injected from `ToolRegistry.Resolve("fpcalc")` at startup. Shipped PR #1465.
- [x] **TOOL-4** `OllamaDaemon` — PID-file adoption, `EnsureRunningOrAdopt`, `StopWhenIdle`, supervise goroutine (health check, crash restart, graceful stop). Shipped PR #1465.
- [x] **TOOL-5** `EmbedQueue` — buffered channel + debounce `time.Timer` drain; `Start/Stop/Enqueue/DrainNow`. Configurable debounce interval in `ToolsConfig`. Shipped PR #1465.
- [x] **TOOL-6** `EmbeddingClient.SetOllamaAvailable` gates local-embed path; `SetResolvedFpcalcPath` gates fingerprinting; reembed op checks `toolRegistry.Available("ollama")`. Shipped PR #1465.

## 🧙 Startup Wizard — Tool Install & Config Flow — Captured 2026-06-15

- [x] **WIZ-1** WelcomeWizard step 2: RadioGroup "Install recommended tools" vs "Let me choose" — recommended path shows `<ToolsPanel mode="wizard" />` with Install buttons. Shipped PR #1465.
- [x] **WIZ-2** `ToolsPanel` exposes per-tool status, resolved path, version, and Install action. Advanced fields (debounce interval, managed dir) gated behind `useAdvancedSettings` toggle in Settings → Tools tab. Shipped PR #1465.
- [x] **WIZ-3** Recommended install is the default RadioGroup selection; skippable via "Skip this step" button. Shipped PR #1465.

## 🔌 Operations → Pluggable Workflow System — Captured 2026-06-15 [EXPLORATORY — needs brainstorming → spec]

> **Devil's-advocate analysis, pros/cons, Go-library survey, recommended incremental path:** [`docs/research/2026-06-15-tool-lifecycle-and-workflow-system.md`](docs/research/2026-06-15-tool-lifecycle-and-workflow-system.md)
> **Stance:** vision is right and achievable as an *evolution of UOS* (we already have ~80%: op registry + plugins + PR #1440 dependency-scheduling). Resist adopting a heavyweight external engine (Temporal/Conductor break single-binary deploy; go-workflows is code-only) or extracting to a standalone package before the model is proven. **No code yet — core-infra blast radius.**

- [ ] **WF-0** Run a dedicated brainstorming → spec session before any code (per CLAUDE.md plan-first).
- [x] **WF-1** Land PR #1440 dependency-scheduling (prerequisite). See [[project_uos_dependency_scheduling]]. — ✅ verified done 2026-07-01: UOS dependency-scheduling landed flag-off (commit 8282f818, M4)
- [ ] **WF-2** Action-level **capability/requirement declarations** (`requires: [ollama, openai, fpcalc]`) → powers conditional skip/gating (and TOOL-6's auto-gate).
- [ ] **WF-3** Introduce a persisted **`Workflow`** object = enable/disable/schedule-able composition (DAG/ordered) of registered ops. Seed built-in workflows from today's scheduled ops. Collapse `scheduled_*` + `dedup_embeddings_enabled` flags into workflow state. Built-in workflows auto-enabled; user-added start **disabled** until explicitly enabled.
- [ ] **WF-4** Registration-time dependency checks: refuse to register an action that invokes another plugin's actions without declaring the dependency (best-effort runtime check — true static isolation isn't feasible with Go compile-time plugin registration).
- [ ] **WF-5** **UI workflow builder** — LAST, once the backend model is proven (smart-home / CI-CD-style composition). Biggest single cost; treat as its own product surface.
- [ ] **WF-6** (Re-evaluate only) adopt `go-workflows` *iff* durable mid-run crash recovery becomes a hard requirement; (re-evaluate only) extract to a standalone Go package *iff* the model stabilizes and a second consumer wants it.

---

## 🔮 Needs Serious Planning — Open Audiobook Acoustic-Fingerprint Index (a community-owned "AcoustID for audiobooks")

> Captured 2026-06-13. **Not yet specced — needs a dedicated brainstorming → spec session.**
> Related: [`docs/specs/2026-06-13-dedup-tuning-dataset-design.md`](docs/specs/2026-06-13-dedup-tuning-dataset-design.md)

**Vision.** MusicBrainz/AcoustID model audiobooks poorly (their DB is song/recording-based; a 9-hour book is not a "recording"). Build our own, better, **community-usable** index of audiobook acoustic fingerprints + verified identity (title/author/series/narrator/edition). It should be good enough that submitting back to AcoustID becomes unnecessary.

**Why a Git repo as the store (the constraint that shapes everything).** No public server, no hosting budget. A **GitHub repository + GitHub Actions is free, durable, and world-pullable**:
- **Disaster recovery:** if our prod data is wiped, the organizer rehydrates its identity layer by pulling the repo — the index lives outside our box.
- **Distribution:** anyone can clone it and skip the manual fingerprint/identify work we do today.
- **Provenance:** every record change is a reviewable commit/PR — human-verified records are auditable.

**Open design questions for the planning session:**
- **Format** — a sane, diffable, version-controlled on-disk layout (sharded JSON/JSONL by fingerprint-prefix? Parquet? a checked-in SQLite/Pebble snapshot artifact?). Must stay mergeable (avoid giant single files that conflict). Possibly a parallel **AI-queryable representation** (embeddings / structured docs) so the index can be asked natural-language questions.
- **The PR-bot loop** — a process that emits **PRs of new human-verified records**, and **CI workflows that validate (schema, dedup, no-regression) and apply** them to the canonical index. Bounded batch sizes, signed/attributed records, conflict resolution rules.
- **Identity unit** — what a "record" keys on (whole-book signature from `internal/fingerprint/book_signature.go` + part fingerprints + metadata), and how editions/abridgements/re-narrations are represented.
- **Trust & governance** — who can merge, how bad records are challenged/reverted, license (so the world can actually use it).
- **Relationship to AcoustID submission** — likely **supersedes** it; keep submission as an optional downstream export, not a dependency.

---

## ✅ Fable 5 Full-System Review — COMPLETE (June 9–10, 2026)

All 27 planned tasks + 1 bonus task shipped. Specs and plan docs:
- [`docs/specs/fable5-review-findings.md`](docs/specs/fable5-review-findings.md) — 3 CRITICAL / 6 HIGH / 8 MEDIUM / 2 LOW
- [`docs/specs/fable5-spec-itunes-writeback-hardening.md`](docs/specs/fable5-spec-itunes-writeback-hardening.md)
- [`docs/specs/fable5-spec-unified-dedup-pipeline.md`](docs/specs/fable5-spec-unified-dedup-pipeline.md)
- [`docs/specs/fable5-spec-memory-db-optimization.md`](docs/specs/fable5-spec-memory-db-optimization.md)
- [`docs/plans/fable5-implementation-plan.md`](docs/plans/fable5-implementation-plan.md)

### P2 — iTunes writeback hardening ✅ all done
- [x] **F5-T001** Fix LE parser mhoh descent — track string fields now parsed in LE libraries ✅ Jun 9
- [x] **F5-T002** Golden-corpus mhoh encoding audit tool + constants table ✅ Jun 10
- [x] **F5-T003** ⚠ `ITLSafetyContract` — 8 named guards + 13-test regression suite ✅ Jun 10
- [x] **F5-T004** ⚠ `SafeWriteITL` atomic write + header count regeneration (CRIT-3) ✅ Jun 10
- [x] **F5-T005** ⚠ iTunes-conformant string encoders — stop writing +27∈{1,3} (CRIT-1) ✅ Jun 10
- [x] **F5-T006** ⚠ `LocationPair` — 0x0D Windows path / 0x0B URL normalization (CRIT-2) ✅ Jun 10
- [x] **F5-T007** itl-diff/itl-check honesty: msdh inventory, playlist membership, AuditITL ✅ Jun 10
- [x] **F5-T008** Diff-before-write in writeback batcher (HIGH-3) + library-not-in-use gate ✅ Jun 10
- [x] **F5-T010** Fail-closed inflate cap (MED-7) ✅ Jun 9

### P1 — Unified dedup pipeline ✅ all done
- [x] **F5-T011** `internal/dedup/unified` — Signal/UnifiedDedupScore/ComposeScore (noisy-OR v1) ✅ Jun 9
- [x] **F5-T012** ⚠ LSH `fpidx:` Pebble index — build op + write/delete hooks (`lsh_index_v1_done`) ✅ Jun 10
- [x] **F5-T013** LSH probe collector; retire `ACOUSTID_FUZZY_ENABLED` O(N) path ✅ Jun 10
- [x] **F5-T014** Collector refactor + `PairEligibility` + NEW metadata-fuzzy collector ✅ Jun 10
- [x] **F5-T015** ⚠ Candidate schema additions + legacy-fingerprint purge (~14K stale 100% rows) ✅ Jun 10
- [x] **F5-T016** API: band/score/breakdown fields, `/breakdown`, `/rescore` ✅ PR #1414
- [x] **F5-T017** Unified Dedup UI tab (flag removed; always live after backfill) ✅ PR #1416
- [x] **F5-T018** Scan op rationalization (merge embed-scan/async; phase ordering) ✅ Jun 10

### P3 — Memory & DB optimization ✅ all done
- [x] **F5-T019** ⚠ Strip AcoustIDSeg0–6 from memdb (~550–900MB RSS) ✅ Jun 10
- [x] **F5-T020** ✅ Drop seg fields from `book_file:` Pebble values (sweep + `bookfile_seg_drop_v1_done`) ✅ Jun 10
- [x] **F5-T021** Embedding float16+zstd (`emb_f16_v1_done`, dual-read) ✅ Jun 10
- [x] **F5-T022** Remove legacy SQLite store (~7.9K lines + CGO dep) ✅ Jun 10
- [x] **F5-T023** memdb size telemetry + operation-log retention + dead-prefix sweep ✅ Jun 9
- [x] **F5-T024** NutsDB → Pebble activity/metrics migration (dual-write window) ✅ Jun 10

### P4 — Fixes ✅ all done
- [x] **F5-T009** accept-invite HTTP/2 EOF fix + 413 clarity (resolves pen-test MED-5) ✅ Jun 9
- [x] **F5-T025** `FilterUnchangedTags` covers custom `AUDIOBOOK_ORGANIZER_*` tags ✅ Jun 10
- [x] **F5-T026** Duration/filesize aggregation from BookFiles + backfill ✅ Jun 10
- [x] **F5-T027** Chromem hydration shutdown join ✅ Jun 10

### Bonus
- [x] **F5-T028** `AppConfig` RWMutex-guarded accessors — convert all write sites ✅ Jun 10

---

## ✅ Dedup Tuning Dataset — COMPLETE (June 13, 2026)

Spec: [`docs/specs/2026-06-13-dedup-tuning-dataset-design.md`](docs/specs/2026-06-13-dedup-tuning-dataset-design.md)
Plan: [`docs/plans/2026-06-13-dedup-exact-gate-and-dataset.md`](docs/plans/2026-06-13-dedup-exact-gate-and-dataset.md)

### M0 — Legacy false-positive purge ✅ applied on production June 13
- [x] **M0** `dedup.purge-legacy-fp-candidates` applied: 12,322 candidates (`layer=exact`,
  `similarity=1.0`) → `stale-fp`. Idempotency flag `dedup_fp_purge_v1_done` set. ✅ Jun 13

### Milestone A — Engine gate ✅
- [x] **A1** `hasPlausibleAudio(book *database.Book) bool` in `internal/dedup/engine.go`:
  returns true when `Duration > 0` OR `FileSize >= 256 KiB`. ✅ Jun 13
- [x] **A2** Gate applied to `checkExactTitle` (both sides, before emit). ✅ Jun 13
- [x] **A3** Gate applied to `checkExactISBN` (both sides, before emit). ✅ Jun 13
  Note: `checkExactAcoustID` intentionally not gated (AcoustID is its own evidence).

### Milestone B — Dataset store + builder + catchers ✅
- [x] **B1** `internal/database/dedup_label.go`: `LabeledExample`, `BookFeatures`,
  `LabeledExampleFilter`; PebbleDB keyspace `dedup:label:<id16hex>`. ✅ Jun 13
- [x] **B2** `*EmbeddingStore` methods: `UpsertLabeledExample`, `GetLabeledExample`,
  `ListLabeledExamples`, `CountLabeledExamples`. ✅ Jun 13
- [x] **B3** `internal/dedup/dataset/builder.go`: `BuildExample` (pure; computes duration
  ratio, folder relation, recording-ID overlap, signature relation). ✅ Jun 13
- [x] **B4** `internal/dedup/dataset/rules.go`: `Classify` — three catchers in priority
  order: `wholeBookSignatureMatch` → `true_dup`; `missingFile` → `not_dup`;
  `partVsWhole` (ratio < 0.5) → `not_dup`. ✅ Jun 13

### Milestone C — Backfill op ✅
- [x] **C1–C4** `internal/plugins/dedup/dataset_backfill.go`: `dedup.dataset-backfill` UOS
  op. Dry-run by default; `apply=true` writes and suppresses rule-labeled `not_dup`
  candidates. Idempotent. ✅ Jun 13

### Deferred follow-ups (open)
- [x] **C5-gate** FileSize-aware catcher for residual stub pairs ✅ Jun 13 — `implausibleAudio`
  catcher (`internal/dedup/dataset/rules.go`): labels `not_dup` when a side has zero/unknown
  duration AND a largest file below the 256 KiB stub floor; `BookFeatures.FileSizeBytes` added
  + populated. Catches the dominant residual class (0-second stubs) that `partVsWhole`
  (`DurationRatio == 0`) and `missingFile` (file records exist) miss; genuine unscanned-large
  copies are not suppressed. Run `dedup.dataset-backfill --apply` to clear the existing ~3,154.
- [x] **C5-sig** Offset/subsequence containment: `signatureRelation` currently returns only  ✅ shipped #1717 (agent-task sweep 2026-07-01)
  `match`, `disjoint`, or `unknown`. The `a_contains_b` / `b_contains_a` values in the
  spec require comparing signature subsequences — deferred to a future milestone.
- [x] **C5-folder** `sibling_parts` folder relation: `folderRelation` currently returns only  ✅ shipped #1721 (agent-task sweep 2026-07-01)
  `unrelated`, `same_dir`, `a_ancestor_of_b`, `b_ancestor_of_a`. The `sibling_parts`
  value (same parent, different child dirs matching a series pattern) is planned but not
  yet implemented.
- [x] **C-human / Slice A** Human label capture: merging or dismissing a dedup candidate now
  writes a gold `LabeledExample` (`label_source="human"`, `true_dup`/`not_dup`) via
  `internal/server/handlers/dedup/label_capture.go`. Best-effort (never blocks the action);
  merge snapshots features pre-merge. Hooked: single merge, single dismiss, bulk merge,
  cluster dismiss. **Follow-up (deferred):** cluster-merge + series-merge capture (need
  pre-merge snapshot reordering); `RemoveFromDedupCluster` → not_dup capture.
- [x] **C-gold / gold miner** In-house high-confidence positive miner: op `dedup.mine-gold-labels`
  (`internal/plugins/dedup/mine_gold_labels.go`) labels pending candidates `true_dup`
  (`label_source="auto_high_conf"`) when the two books share a file hash, AcoustID recording id,
  or ASIN/ISBN (audio-gated). Pure matcher `dataset.MineHighConfidenceDup`
  (`internal/dedup/dataset/highconf.go`), 7 unit tests + 2 op e2e tests. Dry-run default;
  reuses candidate ids (no synthetic rows). Complements `dedup.dataset-backfill` (rule negatives)
  + human capture. **Not yet run on prod** — dry-run first via `{"def_id":"dedup.mine-gold-labels","params":{}}`.
- [x] **C-rebuild / gold rebuild** New op `dedup.rebuild-gold-labels`
  (`internal/plugins/dedup/rebuild_gold_labels.go`) re-derives `label_source="rule"`
  (`dataset.Classify`) and `label_source="auto_high_conf"` (`dataset.MineHighConfidenceDup`)
  labels against current candidate/book/embedding state — the 6,095-row label store
  (5,050 rule / 248 auto_high_conf / 3 human / 794 unlabeled) predates the
  CONS-16/17/FRAG fixes and the bge-m3 cutover, so a chunk of the mechanical
  labels currently reference merged/deleted/non-primary books or catchers that
  no longer fire. This is a gold-label-quality fix only — it does **not**
  address `dedup.calibrate-embedding-thresholds`'s `skipped_dim` count
  (2,841/5,301 pairs on the 2026-07-04 prod run), which is a separate,
  still-open embedding-side staleness problem (stale `.Model`/dimension
  mismatches on stored embeddings, root cause not yet found — do not conflate
  the two). Dry-run reports changed/unchanged/unlabelable counts per bucket;
  `apply=true` deletes+reinserts the rule/auto_high_conf rows (idempotent),
  never touching `label_source="human"` or unlabeled (`LabelSource==""`) rows.
  New `EmbeddingStore.DeleteLabeledExamplesBySource` primitive. 3 unit tests.
  **Not yet run on prod** — dry-run first via `{"def_id":"dedup.rebuild-gold-labels","params":{}}`,
  review the diff, only then `apply=true` (owner greenlight).
- [x] **C-orphan-embed / DeleteBook orphaned-embedding fix (2026-07-04)** —
  `PebbleStore.DeleteBook` (`internal/database/pebble_store.go:1735`) deleted
  the book row + path/version-group/metadata_state/ISBN/ASIN index rows but
  never touched the book's embedding row (`emb:v:book:<id>`). Since
  `dedup.embed-scan` only iterates `GetAllBooks`, a deleted book's embedding
  was orphaned forever at its last model/dimension and never re-embedded.
  This is **likely the dominant contributor** to the `skipped_dim` count
  above (2,841/5,301) — directionally consistent with the rebuild-gold-labels
  3,525/5,050-unlabelable finding — but is **not confirmed to be the sole
  cause**; treat `skipped_dim` root-cause as still open until verified
  post-deploy. Fix: delete `emb:v:book:<id>` in the same batch as the rest of
  `DeleteBook`'s cleanup (atomic, no new `EmbeddingStore` plumbing). Verified
  all merge/consolidate "loser book" removal paths (`internal/dedup/engine.go`,
  `internal/dedup/book_dedup.go`, `internal/dedup/split_book_merge.go`,
  `internal/merge/service.go`'s `SoftDeleteBook` hard-delete fallback) route
  through `Store.DeleteBook`, so no separate path needs the same patch. New
  test `TestPebbleDeleteBook_RemovesEmbedding`. **Forward-only** — does NOT
  retroactively clean up already-orphaned embedding rows from historical
  deletions; that needs its own dry-run-gated backfill/maintenance op
  (tracked as a follow-up, not built here — see below).
  - [x] **Follow-up built (2026-07-04):** `dedup.cleanup-orphan-embeddings`
    (`internal/plugins/dedup/cleanup_orphan_embeddings.go`) — dry-run-gated
    op that walks every `emb:v:book:*` row via the existing
    `EmbeddingStore.ListByType("book")` (no new DB primitive needed), checks
    `GetBookByID` per entity ID, and reports orphaned/live/lookup-error
    counts + a bounded 10-row sample of orphaned IDs + `.Model`. `apply=true`
    deletes only confirmed-orphaned rows; a live book's embedding is never
    touched regardless of model (that's `dedup.embed-scan`/
    `dedup.reembed-embeddings` territory, explicitly out of scope here).
    Idempotent (second apply finds 0 orphans). 5 unit tests in
    `internal/plugins/dedup/cleanup_orphan_embeddings_test.go`. **Not yet run
    on prod** — dry-run first via
    `{"def_id":"dedup.cleanup-orphan-embeddings","params":{}}`, review the
    sample, only then `apply=true` (owner greenlight).
  - [ ] **Follow-up (owner-gated, post-deploy):** run
    `dedup.cleanup-orphan-embeddings` dry-run on prod, review, apply; then
    re-run `dedup.embed-scan` and `dedup.calibrate-embedding-thresholds` and
    confirm `skipped_dim` trends down (both from #1802 stopping new orphans
    AND from this op clearing pre-existing ones).
- [x] **C5** Live-capture: wire `BuildExample` + `Classify` into the candidate-upsert path  ✅ shipped #1729 (agent-task sweep 2026-07-01)
  so each new candidate automatically gets a feature snapshot + deterministic label on
  creation (no separate backfill needed going forward).
- [x] **C6** Review UI: web panel listing `dedup:label:` examples filterable by label, — ✅ verified done 2026-07-01: label_review.go + web DedupLabels.tsx (filter + override)
  label_source, band; human can override label and set `label_source=human`.
- [x] **C7** JSONL export: admin endpoint or CLI tool to export labeled examples as JSONL  ✅ shipped #1730 (agent-task sweep 2026-07-01)
  for offline ML training; include `formula_version` for dataset versioning.
- [ ] **C8** Auto-bug-filing: after backfill, emit a GitHub issue per `not_dup` cluster
  where rule-suppressed count exceeds a threshold (surfacing systematic false-positive
  sources for human review).

---

## ✅ Completed — June 9, 2026

- [x] **BURNDOWN-REBASE** Burndown bot: automatic conflict resolution — `rebase-stale` job
  rebases CONFLICTING bot PRs onto main before each dispatch run;
  `status:conflict-unresolvable` label + comment for true conflicts.
  (falkcorp/github-common PR #303, v1.11.0; audiobook-organizer PR #1353)
- [x] **BURNDOWN-SCHED** Burndown schedule reliability: dual slot (08:00+20:00 UTC),
  `full` mode for scheduled runs, `max_tasks=8` cap to prevent OpenAI 429.
  (audiobook-organizer PR #1342, v2.5.0→v2.6.0)
- [x] **BURNDOWN-DECOMPOSE** Proactive task decomposition: 16 broad `on-hold` testing
  issues (burndown-tasks #52–#67) closed and replaced with 31 narrow single-file
  issues (#79–#109), each completable within the 90-iteration agent cap.

---

---

## 📚 Project Documentation (TODO — not yet done)

- [ ] **DOCS-1** [hold] Write comprehensive system documentation for `falkcorp/audiobook-organizer` into `docs/` — full process graphs, architecture diagrams, data flow, component inventory, operations runbooks, incident history. Target: ≥9 files, ≥7 Mermaid diagrams (flowchart, sequence, state machine, Gantt). Model after `falkcorp/burndown-tasks/docs/` (PR #73, 2216 lines). Invoke as a dedicated documentation subagent: "write full process graphs, literally all the documentation you can write. The more graphs and charts the better."  <!-- 2026-07-01: ℹ️ quantitative bar already exceeded (418 md files, 59 mermaid); a single cohesive burndown-style deliverable is the only residual. -->

---

## 🔐 Security (pen-test 2026-06-04)

All 11 pen-test findings fixed:

- [x] **MED-5** `POST /api/v1/auth/accept-invite` EOF + 413 clarity — fixed in F5-T009 (Jun 9). `ShouldBindJSON` upgraded to explicit body-read + close; 413 response body now includes `{"error":"request body too large","max_bytes":N}`; Gin set to release mode to suppress debug headers. ✅

---

## 🎯 Whole-file fingerprint migration (started May 30, 2026)

PR `feat/fingerprint-wholefile` (Step 1 + 2) ships:
- New `BookFile.AcoustIDFingerprint []byte` + duration sec
- `FileWholeFingerprint(path)` extraction (no seek, no offsets)
- Middle-80% similarity compare (suppresses Audible intros/outros)
- `synthesizeBookSignatureForBook` switched to partial-coverage synth
- Memdb strip of the new fingerprint bytes (RSS protection)

**Post-deploy actions for this PR:**
- [x] Run `acoustid.reset-all` on prod (retires AQAAAA-poisoned segs) — done 2026-05-31, 28,538 fps cleared
- [x] Book-level parallelism (PR #1217, FP_PARALLEL_WORKERS=16) — merged + deployed 2026-05-31
- [x] Run `fingerprint-rescan` on prod — **COMPLETE** 2026-05-31 15:50 UTC-4; 2h45m3s; fp=275,318 skip=0 ineligible=23,826 fail=4,882 (98.3% eligible-file coverage; failures are corrupt/too-short files, unrecoverable)
- [ ] [hold] Verify dedup stops showing 14K false-positive 100% matches  <!-- 2026-07-01: ⏳ purge applied on prod (Jun 13, 12,322 rows); only UI re-verification remains. -->
- [ ] [hold] Verify book-sig coverage % shows up for partial books  <!-- 2026-07-01: ⏳ SynthesizePartialBookSignature computes coveragePct (book_signature.go:255); only UI verification remains. -->

---

## ✅ Security workflow repair (completed May 31, 2026)

- [x] Fix Go dependency submission for Go JSON v2 imports by setting
  `GOEXPERIMENT=jsonv2` in `.github/workflows/security.yml`.
- [x] Remove invalid `go-version-input` from
  `actions/go-dependency-submission`.
- [x] Fix `jdfalk/ghcommon` reusable Security workflow so JavaScript CodeQL uses
  `build-mode: none` instead of unsupported `autobuild`.
- [x] Restore this repo's reusable CodeQL language matrix to
  `["go", "javascript", "actions"]` and remove the local JavaScript CodeQL
  workaround.
- [x] Verified fixed by Security run `26727789014`.

**Follow-up PRs (not in this PR):**
- [x] **Step 3 — LSH index for whole-file similarity.** ✅ F5-T012 + T013 (Jun 10): `fpidx:<subfp>:<bookfile_id>` Pebble secondary index + build op; LSH probe collector replaces O(N) `ACOUSTID_FUZZY_ENABLED` path.
- [x] **Step 4 — Drop legacy seg1..6 fields.** ✅ F5-T019 + T020 (Jun 10): seg fields stripped from memdb projections (T019) and from all new Pebble `book_file:` writes (T020); `SweepBookFileSegDrop` backfills legacy rows.
- [x] **Online AcoustID lookup.** Whole-file fps can now be POSTed — ✅ verified done 2026-07-01: plugins/acoustid/online_lookup.go (/v2/lookup, ≥0.85, API-key gated)
  to `acoustid.org` for MBID enrichment — wire it up as an
  optional enrichment step after fingerprinting.

---

## 🚀 Followups from May 28, 2026 perf sprint

Today shipped 10 PRs (#1147-#1155) fixing the 67GB OOM, slow filtered
queries (4m→100ms), 500-per-page (3m51s→241ms), and the registry
double-dispatch + acoustid.scan subprocess bug. The fixes left a tail
of cleanup and proper-implementation work. Each task below is sized
for a small model (~30 min, single-file scope, clear acceptance test).

### MAYDEPLOY-A: Wire subprocess child-mode handler in main.go
`Isolate: true` on operation defs is currently disabled (PR #1155)
because the subprocess child-mode handler is never wired into
`main.go`. Without it, the child process re-execs with
`--operation-runner` and cobra root errors out with "unknown flag".

- [x] **MDA1** [hold] `main.go`: before `cmd.Execute`, call — ✅ verified done 2026-07-01: main.go:37 registry.IsChildMode() before cobra parse
  `registry.IsChildMode()`. If true, build a minimal Registry with all
  plugin defs (NO server init, NO memdb warm — just defs + store
  access), then call `registry.RunChildMode(r)` which never returns.
  Acceptance: `audiobook-organizer --operation-runner <opID>` with
  `UOS_SOCKET=/tmp/sock` connects to a parent socket and runs.
- [x] **MDA2** ✅ Done (verified 2026-06-17) — `internal/operations/registry/subprocess_test.go`
  already contains `TestSubprocess_HandshakeRoundtrip` + `TestSubprocessRoundtrip` (added
  2026-06-13) which re-exec the test binary as the child and verify handshake + result
  roundtrip over the unix socket. `go test ./internal/operations/registry/ -run TestSubprocess`
  passes. The stale auto-burndown PR #1339 is redundant (left for the user to close).
- [ ] **MDA3** [hold] After MDA1+MDA2 pass in CI, revert `Isolate: false` on the
  7 ops (PR #1155). Restore the original comments. Verify
  acoustid.scan logs both parent "dispatched" AND child stdout
  routed through reporter.

### MAYDEPLOY-B: Dedup UX hardening (today's logs)
The merge button hits TWO endpoints per click, and stale candidate
rows reference merged-away book IDs.

- [x] **MDB1** Find which frontend component fires both
  `POST /api/v1/audiobooks/merge` AND
  `POST /api/v1/dedup/candidates/:id/merge` for one Merge click
  (likely `web/src/components/dedup/`). Pick one endpoint, remove
  the other call.
- [x] **MDB2** `server/dedup_handlers.go:mergeDedupCandidate`: when
  `mergeService.MergeBooks` returns "book not found", respond 409
  Conflict with body `{status: "already_merged"}` instead of 500.
- [x] **MDB3** Add `cleanupCandidatesForMergedBook(bookID)` to
  `internal/dedup/engine.go`: when a book is merged-away, mark
  ALL other candidate rows referencing that book ID as "merged" so
  they disappear from the pending-candidates list. Call from
  `mergeService.MergeBooks` after the book is gone.
- [x] **MDB4** `embeddingStore.ListCandidates`: filter out rows whose
  `entity_a_id` or `entity_b_id` no longer exists in the book table
  (defense-in-depth against MDB3 missing edge cases).

### MAYDEPLOY-C: List-query perf cleanup
The big wins shipped (#1153 GetBookFilesForIDs memdb pushdown), but
the request path still has redundant work.

- [x] **C1** `server/audiobooks_handlers.go:buildAudiobookListResponse`
  calls `GetBookFilesForIDs(bookIDs)` directly AND
  `aggregateFileMetadata` also calls it. Pass the map from one to the
  other, eliminate the duplicate fetch. Saves ~2x for fingerprint
  compute path.
- [x] **C2** `PebbleStore.GetAllBookFiles` still does a full Pebble
  scan. Add a memdb fastpath like #1153 did for
  `GetBookFilesForIDs`. Acceptance: `aggregateFileMetadata` fallback
  path uses memdb when published.
- [x] **C3** Audit ALL `GetAll*` callers in request paths (handlers
  + services). Anything that fetches the full corpus to filter 20
  rows is the same bug class as #1149/#1153. Completed 2026-05-29 —
  see [`docs/perf-audit-2026-05-29-getall-callers.md`](docs/perf-audit-2026-05-29-getall-callers.md).
  8 HOT-BAD, 2 WARM-BAD findings filed as MAYDEPLOY-H1..H8 below.
  Easy `/health` win (`CountAuthors`/`CountSeries`) shipped in the
  audit PR.

### MAYDEPLOY-D: Heap baseline reduction
After the strip (#1152), memdb baseline is ~5GB (down from ~10GB).
Total process baseline is ~18GB, with chromem-go contributing ~6GB
via SQLite-to-chromem hydrate at startup.

- [x] **D1** `internal/dedup/engine.go:HydrateChromem` reads ALL
  `book` embeddings (1.8GB on disk) into memory at startup and
  mirrors to chromem. Add `DEDUP_CHROMEM_LAZY=true` env var that
  skips the eager hydrate; mirror lazy on first FindSimilar call.
- [x] **D2** chromem persistent dir `/var/lib/audiobook-organizer/chromem`
  is empty (1KB). Either fix chromem-go persistence so we don't
  re-hydrate from SQLite each restart, OR remove the
  `NewPersistentDB` call and use `NewDB()` (clearer intent).
- [x] **D3** Description / NotesJSON / BookSigV1 stripped from
  memdb in #1152 mean `field:description` filters silently return
  zero. Add a Pebble-backed fallback in
  `audiobooks/service.go:matchesFieldFilters` that fetches the
  full Book via GetBookByID ONLY when the predicate field is
  stripped — preserves correctness on rare descriptions filter.
- [x] **D4** Profile: trigger a fresh memdb warm, capture
  `inuse_space` heap profile, compare to pre-strip baseline. Confirm
  ~5GB drop matches expectations. File any remaining hot allocators
  as new D-tasks. — Structural audit (no live prof access) at
  `docs/perf-audit-2026-05-29-heap-breakdown.md`. Predicted memdb
  drop ~6.7 GB (8.5 GB → 1.7 GB). Observed RSS drop 67 → 39 GB ≈ 28 GB
  (GC headroom + arena release amplify the live-heap delta). Followups
  filed as MAYDEPLOY-I below.

### MAYDEPLOY-I: Heap baseline follow-ups (from D4 audit)
Structural audit (`docs/perf-audit-2026-05-29-heap-breakdown.md`)
identifies these next-biggest-win targets at the ~18 GB post-strip
baseline.

- [ ] **I1** [hold] Verify MAYDEPLOY-D1 (`DEDUP_CHROMEM_LAZY`) and D2  <!-- 2026-07-01: ⏳ D1/D2 code shipped (DEDUP_CHROMEM_LAZY lifecycle.go:48); prod pprof verification remains. -->
  (chromem persistence vs `NewDB()`) ship before any more memdb
  strips. Chromem is the largest remaining bucket (~6 GB live;
  3–6 GB savings projected).
- [x] **I2** Drop `works` table from memdb entirely. 211 K rows ×
  (~270 B/row + ~320 B index) ≈ 120 MB heap, and Works are queried
  in <0.1% of requests. Route the read paths through Pebble
  (`GetWorkByID`) on demand and delete the table from `memdbSchema()`
  + remove `stripBookForMemdb`-adjacent warmup. Est. savings: ~120 MB.
- [x] **I3** Add `stripBookFileForMemdb` (mirrors #1152). Clear the 7
  `AcoustIDSeg0..6` strings + 3 fingerprint-diagnostic `*string`/
  `*time.Time` fields. ~70 MB savings across 308 K book_files.
  AcoustID is read only by dedup, which already has a Pebble path.
- [x] **I4** Cap the 24h `list` / `facets` / `dedup` / `bookCache` /
  `audiobook_list` LRU caches by entry count (e.g. 1000), not just
  TTL. These hold full `gin.H` / `Book` payloads with descriptions
  and provenance maps; suspected ~0.5–1.5 GB of baseline. Touch
  points: `internal/server/server.go:335-337`,
  `internal/audiobooks/service.go:105-106`.
- [x] **I5** Truncate description text fed to bleve to first ~500
  chars (or skip indexing description entirely). bleve still indexes
  the full description from Pebble even though memdb has been stripped.
  Est. savings: 0.5–1 GB index residency.
- [ ] **I6** Once I1+I2+I3 ship (or chromem D1/D2 lands), re-run this  <!-- 2026-07-01: ⏳ heap-audit-rerun is a prod pprof measurement action, no code deliverable. -->
  audit with a real `inuse_space` heap profile from prod via
  `pprof_endpoint` — replace structural estimates with measured
  bytes. Target: baseline ~18 GB → ~10 GB.

---

## 🧭 Post-Deploy 2026-05-29 — Remaining Work

End-of-day state: MAYDEPLOY A→I shipped except items listed below. 45 PRs
merged today (#1147–#1191). RSS 67.8GB→39.6GB stable. "All Books" cold
~250ms, 500/page 3m51s→241ms. Fingerprint rescan hotfix #1191 in. Op-log
Copy + pause-on-hover in #1182.

### Highest-priority remainders

- [ ] **PD-1 / MAYDEPLOY-A revisit** [hold] — Subprocess isolation via parent-RPC
  bridge. Current `Isolate: true` cannot work because PebbleDB is
  single-writer and the child cannot reopen the store
  (`resource temporarily unavailable` on second open). Two viable paths:
  (a) child runs against a *read-only* Pebble snapshot and routes writes
  back through a unix-socket RPC to the parent's writer, or
  (b) drop subprocess isolation entirely and rely on in-process panic
  recovery + memory caps. Spec: `docs/specs/subprocess-isolation-rpc.md`.
- [x] **PD-2 / BUG-ITUNES-WRITEBACK-CORRUPTS-LIBRARY** — Fixed in PR #1319.
  Root cause: `buildMhohLE` set `headerLen = totalLen` instead of the fixed
  iTunes value of 24. iTunes uses `headerLen` to locate type-specific data
  within mhoh chunks; wrong value caused corrupt-library on next open. Also
  fixed `UpdateMetadataLE` to preserve original `headerLen` via
  `rewriteHohmLocationLE` when replacing existing mhoh chunks.
- [ ] **PD-3 / Post-deploy verification** — confirm in prod:  <!-- 2026-07-01: ⏳ post-deploy prod verification checklist (docs/pd3-prod-verification.md); no code deliverable. -->
  (1) fingerprint-rescan from UI now actually runs (no
  "failed to unmarshal params"), (2) op-log Copy + Refresh buttons work,
  (3) RSS post-I2/I3/I4/I5 holds steady or drops further, (4) chromem
  switch from `NewPersistentDB` → `NewDB` doesn't regress dedup recall.
  See `docs/pd3-prod-verification.md` for the actionable plan and the
  verification table where results should be recorded.

### Deferred from MAYDEPLOY

- [x] **MAYDEPLOY-G5b** — back-fill poisoned `Book.Title` rows ✅ (2026-05-31)
  Op `maintenance.title-backfill` shipped; dry-run by default. Run with
  `{"dryRun": false}` on prod to apply. Old→new logged via op reporter.
- [x] **MAYDEPLOY-G5c** — tighten `groupTracksByAlbum` to use — ✅ verified done 2026-07-01: albumGroupKey empty-Album branch strips chapter prefix/suffix (importer.go:912)
  `stripChapterPrefix(track.Name)` as the album key when Album tag is
  empty. Needs design pass — risks merging unrelated tracks that
  share a stripped prefix.
- [x] **MAYDEPLOY-H5** [hold] — metadata-fetch-ids: when `len(bookIDs) < 100`,  ✅ shipped #1720 (agent-task sweep 2026-07-01)
  use per-book `GetAuthorByID` instead of materialising 8.8K authors.
  Low priority; defer until profiler shows actual cost.
- [x] **MAYDEPLOY-H7** [hold] — Cache `isProtectedPath` / `GetAllImportPaths`  ✅ shipped #1725 (agent-task sweep 2026-07-01)
  with TTL or mutation invalidation. Low priority (~10 rows).
- [ ] **MAYDEPLOY-I1** [hold] — Verify D1 (`DEDUP_CHROMEM_LAZY`) and D2  <!-- 2026-07-01: ⏳ duplicate of I1 — code shipped; prod verification remains. -->
  (`NewDB()`) shipped behaviour matches design. Needs live prod
  observation (`/system/status`, heap dump).
- [ ] **MAYDEPLOY-I6** [hold] — Re-run heap audit with live pprof from prod  <!-- 2026-07-01: ⏳ duplicate of I6 — prod pprof measurement. -->
  via `pprof_endpoint`. Replace structural estimates in
  `docs/perf-audit-2026-05-29-heap-breakdown.md` with measured bytes.
  Target: baseline ~18 GB → ~10 GB.

### Post-Task Hygiene (per CLAUDE.md)

- [x] **PD-4** [hold] Update `CHANGELOG.md` with full MAYDEPLOY A–I sweep + — ✅ verified done 2026-07-01: CHANGELOG.md:1554 documents MAYDEPLOY A→I sweep + Wave 4 (PRs #1156–#1191)
  Wave 4 PRs (#1182–#1191), prepending to current section, not
  overwriting.

---

### MAYDEPLOY-E: Pre-existing test failures
Surfaced during today's deploys but not caused by them; failing on
`main` too.

- [x] **E1** `TestHandler_RenameAuthor_Success` (`server/handlers_unit_test.go:982`)
  panics with nil pointer in `Cache.InvalidateAll`. Test creates a
  Server without initializing `authorsCache`. Fix test fixture to
  use `cache.New[any]("authors", 30*time.Second)`.
- [x] **E2** `TestPebbleStoreReset` (`database/coverage_test.go:841`)
  expects 0 authors after reset, gets 1. Likely a memdb-vs-pebble
  reset-order bug. Reproduce, fix, add regression test.
- [x] **E3** `TestEnrichAudiobooksWithNames_WithAuthorAndSeries`
  (`audiobooks/audiobook_service_unit_test.go:536`) fails because
  `aggregateFileMetadata` calls `GetBookFiles` per book and the
  mock has no expectation. Add `.EXPECT().GetBookFiles(...).Return(...).Maybe()`
  to the fixture.

### MAYDEPLOY-F: Trickle warmer tuning
The warmer's eager phase is fast post-#1153, but the trickle's heap
ceiling logic isn't quite right under sustained background activity
(chromem hydrate, dedup scans).

- [x] **F1** Eager warmer (in `library_list_warmer.go`) has no heap
  guard. If chromem hydrate is concurrent with eager, eager could
  pile on. Add same `readHeapAllocMB() > ceiling` check as trickle.
- [x] **F2** Trickle baseline is sampled once at start. If baseline
  drops over time (e.g., chromem hydrate completes and releases),
  ceiling stays artificially high. Re-sample baseline every 5 min
  (median of last 3 samples to dampen).

### MAYDEPLOY-G: Multi-file audiobook over-split detection + fix

Observed in prod (book `01KQGDQTJ44FCAPW5Z9D2KNQDE`,
`/Tarkin - Star Wars/Tarkin - Star Wars - 4/85.mp3`): the scanner
created **85 separate Book records for what is ONE 85-chapter
audiobook**. Each "book" has exactly one file. Titles like
`(76/85) Tarkin: Star Wars` where the `(76/85)` is fabricated and
doesn't even match the file's own chapter number (file is `4/85`
but Book says `76/85`). All 85 books sit in the same folder with
the same series + author, varying only by chapter number.

This is a different bug class than acoustic dedup — it's
**scanner mis-grouping** (one folder → many books instead of
one book × many BookFiles).

- [x] **G1** Scanner detection at import time:
  `internal/scan/` — when a folder contains N≥3 files matching a
  sequential numeric pattern (`*-N/M.ext`, `Chapter NN`,
  `NN of MM`, `Part NN`, etc.) AND the audio metadata's
  `album_artist`/`album` agree across files, treat as a single
  Book with N BookFiles. Add unit tests covering the
  `N/M`, `Chapter NN`, `Part NN`, `NN of MM`, and bare `NN.ext`
  patterns.
- [x] **G2** Backfill scan operation:
  `dedup.split-book-detector` (new opdef, in-process). Group
  existing Books by `(filepath.Dir(FilePath), author_id,
  series_id)`. Flag any group with ≥3 single-file books matching
  the sequential-naming heuristic above. Write results as new
  `book_split_candidate` rows in embedding store (or new table)
  for review.
- [x] **G3** API + UI for reviewing split candidates:
  `GET /api/v1/dedup/split-candidates` returns flagged groups
  (parent folder + book list + suggested merged title).
  `POST /api/v1/dedup/split-candidates/:id/merge` collapses the N
  books into one (keep oldest book ID, move all files to it,
  delete the rest). UI: new tab in the Dedup page alongside
  acoustic/embedding candidates.
- [x] **G4** One-shot CLI:
  `tools/cmd/merge-split-books/` (mirrors
  `tools/cmd/reconcile-paths/`). Reads split-candidate rows,
  prints dry-run plan, optionally executes. Operator runs once
  against the existing ~thousands of over-split books in the
  library.
- [x] **G5a** Strip leading `(N/M)` / `Chapter N` prefix from
  iTunes per-chapter track Names when fall-back populates
  `Book.Title`. Root-cause analysis:
  `docs/perf-audit-2026-05-29-g5-title-mismatch.md`.
  Source of the `(76/85)` prefix: `buildBookFromAlbumGroup`
  fell back to `firstTrack.Name` when iTunes Album tag was
  empty, writing the per-chapter Name into `Book.Title`. The
  file-vs-title mismatch (file=4, title=76) is a second-order
  artifact from a later organizer/dedup reassignment, not a
  second bug. Fix: `stripChapterPrefix` helper in
  `internal/itunes/service/strip_chapter_prefix.go`, applied
  only in the empty-Album fall-back branch.
- [x] **G5b** Back-fill existing poisoned `Book.Title` rows ✅ (2026-05-31)
  `maintenance.title-backfill` op shipped. Dry-run default.
- [x] **G5c** Tighten `groupTracksByAlbum` to use — ✅ verified done 2026-07-01: albumGroupKey tightened (importer.go:912, commit 7ff363d7)
  `stripChapterPrefix(track.Name)` as the album key when the
  Album tag is empty. Currently every chapter falls back to a
  unique key (the raw Name) and becomes its own book record.
  Deferred — risks merging unrelated tracks that happen to
  share a stripped prefix; needs a separate design pass.
- [x] **G6** Once G1 lands, the legacy `book_files` rows for the
  merged-away books should be cleaned up by G3/G4's merge path —
  but verify orphan rows aren't left in the `book_files` table
  (deleted bookID still has rows). Add a maintenance task
  `maintenance.orphan-book-files-cleanup` that lists `book_file`
  rows whose `book_id` no longer exists, surfaces a count, and
  optionally deletes them.

### MAYDEPLOY-H: GetAll\* pushdown wins from C3 audit

C3 audit findings — see [`docs/perf-audit-2026-05-29-getall-callers.md`](docs/perf-audit-2026-05-29-getall-callers.md).
HOT-BAD callers that fetch the entire corpus to filter a small subset in
synchronous handlers. Same bug class as PR #1149/#1153. The easy 5-line
`/health` win (CountAuthors/CountSeries instead of GetAllAuthors/GetAllSeries)
landed in this audit PR; the rest need a new store method or memdb index.

- [x] **H1** `internal/server/itunes_handlers.go:534,607` — `handleListITunesBooks`
  + writeback-preview load all 50K books to filter by
  `ITunesPersistentID != ""`. Add a memdb secondary index on
  `book.itunes_persistent_id` and a new `ListBooksByITunesPID(limit, offset)`
  store method. Pebble keeps current scan as cold-start fallback.
  Acceptance: `GET /api/v1/itunes/books?limit=20` returns in <100ms hot,
  no full-corpus materialization.

- [x] **H2** `internal/server/deluge_discovery.go:134` — Deluge discovery
  handler loads 308K BookFiles to filter by `DelugeHash != ""`. Switch to
  the existing `store.GetBookFilesNeedingDelugeImport()` wrapper, then
  add a memdb fastpath inside that method (index on non-empty
  `deluge_hash` + null `imported_from_deluge_at`). Mirror the #1153/#1166
  fastpath pattern. Also fixes `internal/plugins/deluge/centralization.go:66`.
  Acceptance: `POST /api/v1/deluge/discover` returns in <100ms hot.

- [x] **H3** `internal/server/entities_handlers.go:118,154` — `listWork` /
  `getWorkStats` use GetAllWorks + per-work `GetBooksByWorkID` (N+1). Add
  `GetWorkBookCounts() map[string]int` (mirrors `GetAllAuthorBookCounts`).
  `listWork` should also paginate. Acceptance: `GET /api/v1/works`
  returns in <200ms with 50K works; `GET /api/v1/works/stats` <50ms.

- [x] **H4** `internal/server/metadata_batch_candidates.go:846` — unfetched
  count loads all Book structs to extract IDs. Add `store.ListBookIDs()
  ([]string, error)` that returns only string IDs (Pebble: iter without
  Value(); memdb: project from books table without copy). Saves ~50×
  memory. Acceptance: `GET /api/v1/metadata/candidates?include_unfetched=true`
  uses <10MB peak vs ~50MB today.

- [x] **H5** [hold] `internal/server/metadata_handlers.go:1283` — metadata-fetch-ids  ✅ shipped #1720 (agent-task sweep 2026-07-01)
  op always materializes 8.8K authors even for 20-book requests. When
  `len(bookIDs) < 100`, use per-book `GetAuthorByID`. Low priority.

- [x] **H6** `internal/scanner/scanner.go:1533,1551` — scanner calls
  `GetAllWorks()` per-book during scan (N² behavior). Build a
  `map[normalizedTitle+authorID]workID` once at scan start, invalidate
  on new-work creation. Cuts scan time on 50K-work corpus by ~10x.

- [x] **H7** [hold] `internal/server/server_middleware.go:90` and  ✅ shipped #1725 (agent-task sweep 2026-07-01)
  `internal/audiobooks/helpers.go:248` — `isProtectedPath` calls
  `GetAllImportPaths()` per-file. Cache with TTL or invalidate on
  import-path mutation. Low priority (~10 rows total).

- [x] **H8** `internal/database/pebble_store.go:8515` —
  `GetBookFilesNeedingDelugeImport` is still a `GetAllBookFiles` wrapper.
  Folded into H2's memdb index work.

### How to fan out
Each task is independent within its parent letter group; A1→A2→A3
must sequence, but A and B are parallelizable. Spawn:
- One **Haiku** agent per task, scoped to the single file noted.
- Each agent owns: branch creation, the fix, build verify, `gh pr create`,
  admin-merge gate (do NOT merge — let a reviewer signoff).
- Coordinator (or the user) merges in MAYDEPLOY letter order so
  Subprocess (A) lands before Dedup UX (B) (B may reuse the
  Isolate path).

---

## 🐛 Open Bugs — May 17, 2026

- [x] **PEBBLE-CLOSED-SWEEPTICK-RESIDUAL** (2026-07-03) — ✅ **FIXED 2026-07-03 in three PRs**
  as the true scope emerged (4 gate kills, 3 distinct legs, all the same leaked-lifecycle family):
  #1778 unconditional sweeper join in Shutdown (2s escape abandoned in-flight sweeps) + ErrClosed
  guard on the two ticker reads; #1781 ROOT fix — registry live-tracker + `testutil/integration.go`
  drains leaked registries before `store.Close()` (server tests closed the store with NO registry
  Shutdown), `recoverPebbleClosed` extended to all ~18 opv2 store methods (dispatcher +
  `UpdateOpProgressV2`/reporter-flush legs), trickle-warmer enrolled in bgWG/bgCtx. Collateral
  finds fixed in #1781: latent prod nil-deref in `ProtectedPathCache.refresh()` with Deluge
  unconfigured (tag-write pre-flights would 500), deluge singleton test leak, RootDir test
  pollution. Proof: `internal/server -short -race -count=2` green (952s).
- [x] **WARMERS-NOT-IN-BGWG** (2026-07-03, follow-up from #1781) — ✅ **FIXED 2026-07-10 (#1794,
  TASK-05).** Sibling fire-and-forget cache warmers (`warmFacetsCache`/`warmLibrarySizes`/
  `warmAuthorsCache`/`warmSeriesCache`) were short-lived and hadn't struck, but shared the
  trickle-warmer's lifecycle gap. Enrolled in bgWG/bgCtx exactly like `runTrickleWarmer`
  (server_lifecycle.go `startCacheWarmers`): each launch now does
  `bgWG.Add(name)`/`defer bgWG.Done(name)` plus a `bgCtx.Err()` shutdown-skip check. New tests
  `TestStartCacheWarmers_SkipOnCanceledCtx` / `TestStartCacheWarmers_EnrolledInBgWG` in
  `internal/server/cache_warmers_bgwg_test.go` cover both the skip-on-shutdown and
  anti-over-suppression (live ctx still runs warmers) cases under `-race`.
- [x] **INGEST-VERSION-FLAKE** (2026-07-03) — ✅ **FIXED #1777.** Root cause was NOT ordering:
  PebbleStore async memdb-warmup race — `CreateImportPath`'s memdb write no-ops before warmup
  publishes, so `GetAllImportPaths` (memdb-backed) missed the test's temp dir →
  `ErrPathNotAllowed`. Fix: `WaitForWarmup()` in the test helper (per its own doc contract).
  Proof: `-count=5 -race -shuffle=on` green. Same family also fixed in #1779's regroup helper.
- [x] **ITUNES-IMPORT-DEDUP-RACE** (2026-07-03) — ✅ **FIXED #1779.** Real prod race, found via
  `TestITunesImport_SkipDuplicates` flake: opv2 status row + `opv2:act:` index written as two
  separate Pebble writes → `EnqueueOp` ConcurrencyKey dedup could observe a completed op still
  indexed active and return its dead op ID (second import's legacy op stuck `queued` forever).
  Fix: atomic batch. Repro red 3/3 pre-fix, green 3/3 post (-count=30 -race).
- [x] **CI-10M-TIMEOUT** (2026-07-03) — ✅ **FIXED #1780.** Minimal CI's reusable workflow ran
  `go test -short -race ./...` with Go's 10m default; internal/server exceeds it on runners.
  github-common had the `-timeout 30m` fix since v1.12.1+ (written FOR this repo) but the pin
  was never bumped. All 5 workflow pins updated to `1dec34cd`.
- [ ] **SDKGUARD-VIOLATION** (2026-07-03) — `pkg/plugin/sdk` imports `internal/logger`, so
  `make ci` fails on main at the `sdkguard` step (masked all session by `| tail` swallowing exit
  codes). Either break the import or add an allowlist entry in `tools/cmd/sdkguard/main.go` with
  justification.
- [ ] **STATICCHECK-BURNDOWN** (2026-07-03) — ~18 pre-existing findings remain after the partial
  cleanup in #1767; `make ci`'s staticcheck step fails on main until drained. Good Haiku-sweep
  candidate.
- [ ] **MOCK-FRESHNESS-GLOB-GAP** (2026-07-03) — the Mock Freshness CI gate's `internal/*/mocks/`
  glob misses nested mocks dirs (e.g. the dir holding `mock_dedup_engine.go`, stale since #1736
  until hand-regenerated in #1757). Widen the glob to `internal/**/mocks/`.
- [x] **PEBBLE-CLOSED-SHUTDOWN-RACE** (2026-07-01, found during the agent-task sweep) — ✅ **FIXED
  2026-07-02** (branch `fix/pebble-shutdown-race`). **Root cause was NOT `SweepTick`** as the
  original entry guessed — that path was already enrolled in `goroutineWG` and drained by
  `Shutdown`. Reproduced with a real PebbleStore (`shutdown_race_test.go`, 10/10 panic pre-fix):
  the actual leak is the fire-and-forget dep-notify goroutines at `registry.go:210,226`
  (`notifyDepCompletion`/`notifyDepFailed`). They used `context.Background()` and were **not**
  enrolled in `goroutineWG`, so `Registry.Shutdown()`'s `goroutineWG.Wait()` returned without
  draining them → `OnOpCompleted → RecordOpCompletion` read/wrote the store after `store.Close()`
  → `pebble: closed` crash. Fix: enroll both notify goroutines in `goroutineWG`, gated under `r.mu`
  against a new `notifyStopped` flag that `Shutdown` sets (under `mu`) just before `Wait()` — this
  closes the Add-after-Wait window created by `releaseRunHandle` removing the op from `r.running`
  (worker.go:303) *before* the notify call (worker.go:331). Late notifies during teardown are
  skipped safely (terminal status already persisted; next `Start`'s `SweepTick` re-evaluates).
  Verified: repro 10/10 pass under `-race`, full `internal/operations/registry` package green under
  `-race`. Not the cause of the two deflaked tests (#1711/#1713) — a separate lifecycle bug.
- [x] **CI-FRONTEND-UNITTESTS-STALE** (2026-06-13) ✅ **FIXED 2026-06-17** — `UnifiedDedupTab.test.tsx`
  updated: `mockCandidate` now carries inline `book_a`/`book_b` (the `include_books` shape)
  and the 2 stale tests assert on the rendered card **title** (`Dune (FLAC rip)`) instead of
  the raw ULID; the bulk-bar test selects the row checkbox via `within(row)` (robust against
  the toolbar filter + header select-all checkboxes) instead of a brittle `getAllByRole` index.
  Full frontend suite green (35 files / 246 tests). No `getSystemStorage` warning surfaced.
- [x] **CI-BACKEND-RACE-TIMEOUT** (2026-06-17) ✅ **FIXED** — `make test` (full `go test ./... -v -race`,
  reused by `test-nightly`) failed with `panic: test timed out after 10m0s` in `internal/server`.
  Root cause: that package runs ~421s **without** `-race` (heavy per-test setup: Pebble + migrations
  + op-registry workers) and exceeds the 600s/package default under `-race`. Fix: `-timeout 25m` on
  the full target. CI's `-short -race` job fits the default and was already green, so this never
  blocked PRs — only local `make test` + nightly.
- [x] **NUTSDB-CLOSE-GOROUTINE-LEAK** (2026-06-17, low priority) — ✅ **INVESTIGATED/DOCUMENTED
  2026-07-01** (not fixed — remains an accepted, benign, third-party limitation).
  `NutsActivityStore.Close()` → `nutsdb.DB.Close()` leaks **1 background goroutine per Open** (the
  TTL time-wheel; isolation micro-test: 20 open+close cycles → 20 survivors). **Benign in prod** —
  the activity store is a process-lifetime singleton (one open at startup,
  `internal/activity/register.go`). Only the test suite (which opens many short-lived stores)
  accumulates them. Do NOT fork/upgrade nutsdb to chase this — `nuts_activity_store.go` is coupled
  to v1.1.0-specific error sentinels (`ErrNotFoundBucket` vs `ErrBucketNotFound`) and an upgrade
  risks breaking empty-scan handling. Re-verified 2026-07-01: `nuts_activity_store.go` has no
  `go func(...)` of its own — `Close()` is a one-line passthrough to `s.db.Close()`, which already
  calls `tm.close()` internally; the goroutine lives entirely inside the vendored nutsdb v1.1.0
  library (`db.go`: `go db.tm.run()`). Confirmed `nutsdb@v1.1.0/options.go` has no option to skip
  the TTL manager (only `ExpiredDeleteType`: `TimeWheel` vs `TimeHeap`, both goroutine-based), so
  the "add an option to skip the TTL manager" mitigation is unavailable without forking, which is
  forbidden. A doc comment was added above `NutsActivityStore.Close()` recording this so the entry
  isn't mis-read as a fixable bug in our own code. If it ever matters, share one store across the
  server test package (test-only change, out of scope here).

- [x] **BUG-ITUNES-WRITEBACK-CORRUPTS-LIBRARY** — Fixed in PR #1319 (2026-06-05). See PD-2.

  **Bisect hint (user, 2026-05-28):** iTunes writeback was working at some point in the past. Find when active feature work on `internal/itunes/` stopped — the breaking change is most likely in the refactor/security/perf commits that came AFTER the last functional feature commit. Candidates to bisect first (newest → oldest):
  - `ee180f84 perf(itunes): implement streaming XML parser for backfill operation` — most likely. Streaming XML changes how we read/emit plist structures; subtle byte-output differences would corrupt .itl.
  - `8c7269af fix(itl): add size cap before uint32 buffer allocations (SEC-AUDIT-8 #468)` — buffer caps on writes could silently truncate atom data.
  - `03380992 fix(security): validate ITunesLibraryWritePath before passing to ITL read funcs (SEC-AUDIT-4b)` and `7b07f17e fix(security): break taint chain in iTunes/audiobook path handlers (SEC-AUDIT-4)` — path normalization side effects (e.g., resolving symlinks could change what we open/write).
  - Last known-good baseline: `f2856e45 feat(itunes): full ITL rebuild-from-DB + partial export (Tasks 033/035)`.

  Procedure: `git checkout f2856e45 -- internal/itunes/` into a worktree, build, attempt writeback against a SAFE copy of an .itl, confirm iTunes accepts it. Then `git bisect` from there to `main`.

- [x] **BUG-STORAGE-PCT-WRONG** (2026-05-20) ✅ Fixed 2026-05-25.

- [x] **BUG-DEDUP-SAMEDIR** Embedding dedup flags chapter files from the same directory as 100% duplicates. ✅ Fixed PR #1001. Multi-file audiobooks split into segments (e.g. `011.mp3`, `062.mp3`) share identical text embeddings and score 100% similar. Fix: add `filepath.Dir(A.FilePath) == filepath.Dir(B.FilePath)` guard in `internal/dedup/engine.go` emission loop (~line 840) and in `PurgeStaleCandidates` (~line 1446). The `bookMeta` struct needs a `filePath string` field.

- [x] **BUG-RECONCILE-OPID** Reconcile tab hits `GET /api/v1/operations/undefined/status` ✅ Fixed PR #1000. Deploy pending. because the POST response wraps the op in `{data: {op_id: "..."}}` but the frontend was reading the raw body as an `Operation`. **Fix shipped in PR #1000** (`startReconcileScan` now extracts `.data` and normalizes `op_id → id`). Needs production deploy.

- [x] **BUG-SERIES-COUNT** Series dedup tab shows "Total series: 0" even when a scan just found 2442 duplicate groups. ✅ Fixed PRs #1008 (band-aid: UpdateOperationStatus on scan complete) + #1009 (proper fix: getOperationStatus falls through to v2 registry; scan handlers no longer create legacy ops).

- [x] **BUG-ACTIVITY-MISSING-OLD-LOGS** ✅ Fixed in PR #1020. Activity log now backfills old `system_activity_log` entries (pre-May 12) on server startup. Migration is idempotent and includes test coverage (`TestMigrateSystemActivityLogs`). Field mapping: `created_at → timestamp`, `message → summary`, `tier="system"`, `type="system_log"`, `tags=["legacy", "system_activity_log"]`.

- [x] **INFRA-OPENTELEMETRY** ✅ Shipped PR #1022. Add OpenTelemetry instrumentation for metrics, spans, and traces. Implemented:
  - `go.opentelemetry.io/otel` + SDK + OTLP gRPC exporter
  - HTTP layer instrumentation via `otelgin` middleware
  - DB instrumentation: `InstrumentedActivityStorer` wrapper
  - Operation execution instrumentation with root spans
  - AI/external call instrumentation helper (`WithOpenAISpan`)
  - Dedup engine spans for `FullScan`, `CheckBook`, `PurgeStaleCandidates`
  - Prometheus metrics endpoint at `/metrics`
  - Config: `OTEL_EXPORTER_OTLP_ENDPOINT` env var; disabled by default

- [x] **BUG-OP-SPARSE-LOGS** (PR #1014) Operations emit almost no log messages to the activity log — only a final result line. Every operation should emit at minimum: (1) start message with scope/count, (2) progress phase-change messages (e.g. "scanning", "comparing", "writing"), (3) per-item or per-batch progress every ~10%, (4) completion summary with counts (processed/skipped/errored), (5) any error/warn lines. Target 4–8 log lines per operation for short ops, more for long ones. Fix: audit every `op.Run(ctx)` handler in `internal/server/` and ensure `EmitInfo`/`LogBatch` calls are present at each phase. Use existing `activity.EmitInfo(w, opID, type, source, msg)` API.

- [x] **FEAT-ACTIVITY-RICH-TAGS** ✅ Implemented in PR #1021. Activity log entries now auto-enrich with structured tags at write time:
  - `op:<op_id>` — ties every log line to its operation
  - `book:<book_id>` — ties to specific book
  - `action:<verb>` — metadata-apply, tag-write, import, reconcile, fingerprint, dedup, organizer, purge, cover-update, maintenance, write-back, scan
  - `outcome:ok|warn|error|skip` — derived from Level field
  - `source:<subsystem>` — itunes, acoustid, openai, openlibrary, scanner, scheduler, etc.
  - `scope:book` — entity type affected (simple heuristic)
  - Backend: `EnrichTags()` in `internal/activity/api.go`, called from `Service.Record()` before store write. No call-site changes needed.
  - Frontend: multi-select tag chip filter UI in ActivityLog.tsx with Outcome and Action preset filters. Tags passed to API with AND semantics.
  - Tests: comprehensive TestEnrichTags with 7 subtests + idempotency + nil handling. All passing.
  - Note: Count refresh after scan is a separate timing issue (auto-refresh interval changes from 5s to 30s when op completes); can address separately if needed.

- [x] **BUG-ACOUSTID-SCAN-OPID** "AcoustID scan queued (op: unknown)" toast ✅ Fixed PR #1000. Deploy pending. because `triggerDedupAcoustID` was reading `raw.id` but backend returns `op_id`. **Fix shipped in PR #1000**. Needs production deploy.

---

## AI Model Configuration

- [x] **AI-MODEL-1** Per-feature LLM model knob — adds `DedupReviewModel`, `MetadataReviewModel`, `FilenameParseModel`, `CoverArtModel` to `config.Config` (defaults `gpt-5-mini`). Replaces hardcoded literals in `openai_parser.go`, `openai_batch.go`, `metadata_llm_review.go`, and `dedup/engine.go` with config getters. PR feat/per-feature-llm-model.

---

## ✅ Completed — May 24, 2026

- [x] **CHAI-SQL-PHASE1-4** Chai SQL migration Phases 1–4 complete. Chai DB opens alongside PebbleDB at startup. Write-through sync (`UpsertBookToChaiDB`) populates `book_files`. `GetBooksBySeriesID_Chai` (Task 3.2) and `GetBooksByAuthorID_Chai` (Task 3.3) implemented with pagination. Denormalized `book:series`/`book:author` prefix indexes removed (Task 3.4) — superseded by Chai SQL variants. `pref_key` column renamed from reserved word `key`. `scanBookFromSQL` NULL handling fixed for non-pointer string fields.
- [x] **PERF-N1-ALL** All 8 N+1 query patterns eliminated: full JSON stored in indices instead of IDs; batch `GetBookFilesForIDs` added; per-object point lookups removed. Critical memory-load paths fixed: `SearchBooks` (was 1M load), `GetDistinctGenres/Languages` (50K load), quarantine/iTunes status queries (100K loads). Quick-query pagination fixed (was applying filters post-page).
- [x] **PERF-CACHE-WARMUP-FIX** Emergency: disabled all cache warm-up goroutines (`warmAuthorsCache`, `warmSeriesCache`, `warmFacetsCache`) after 81GB OOM. Root cause TBD — warm-up objects likely retain full API response objects. `[[project_cache_warmup_memory_fix]]`
- [x] **PERF-AUTHORS-SERIES-CACHE** Authors/series endpoint caching with 24h TTL + mutation invalidation. Response time <100ms from cache (was 3-6 min from N+1).
- [x] **LOG-RECONCILE-PATHS** Convert `tools/cmd/reconcile-paths/main.go` from `log.Printf`/`log.Fatal` to `fmt.Fprintf(os.Stderr, ...)`. Last `log` import in any non-server tool file removed. `go vet ./...` clean.
- [x] **SLOG-W12** Operation context logging end-to-end (PR #1047). New `internal/logging.OpContext` propagated via `context.Context`; `logging.Info/Warn/Error/Debug(ctx, ...)` auto-tag every record with `opID`/`opType`/`opStatus`/`entities`. Wired into 12 ops (metadata-fetch ×2, dedup ×8, library scan/organize/transcode). New endpoint `GET /api/v1/operations/:id/activity`. End-to-end test `TestEndToEndLoggingFlow` captures real slog JSON output and asserts attr propagation. Cleanup: restored 3 maintenance jobs' `reporter.Log` calls W11 dropped; fixed ~30 leftover slog KV-pair vet errors across 8+ files; `go vet ./...` now clean across the whole module.
- [x] **SLOG-W12-UI** Per-operation activity panel (PR #1049). React component consumes `/api/v1/operations/:id/activity`, mounted in `OperationsIndicator` notifications bell. Reusable anywhere.
- [x] **SLOG-W11** Repair W10's incomplete printf → kv conversion: 674 format-string fixes + 134 malformed-message cleanups (PRs #1036, #1037).
- [x] **SLOG-W10** Wave 10 — migrated 265 log.Printf calls across 38 files to slog (PR #1036).

### Library UI follow-ups

- [x] **User-saved quick filters.** Let users save the current filter set as a named preset and surface it in the header kebab menu alongside the six built-in counts. Persist per-user (settings table), include in `/library/quick-queries` payload, edit/delete from a "Manage" submenu.  ✅ shipped #1723 (agent-task sweep 2026-07-01)

### Remaining slog / logging work

- [ ] **SLOG-W13** [hold] Wire `logging.Info(ctx, ...)` into long-tail async ops that currently use raw `slog.Info`: `runBulkWriteBack`, ISBN enrichment goroutine, iTunes sync ops, batch poller, scanner deep paths. ~1363 raw `slog.Info/Warn/Error/Debug` calls across 193 files remain. Priority: any code inside an op-context flow (where `logging.WithOp` has been called upstream). Code outside ops (startup, background goroutines) can stay as raw slog.  <!-- 2026-07-01: ◑ PARTIAL: batch_poller + isbn flows wired (commit 7f5c28f1); runBulkWriteBack, iTunes sync, scanner deep paths remain (~1363 raw slog calls). Re-held per db977ae3 (context overflow). -->  <!-- 2026-07-01: further progress — writeback+ISBN #1715, scanner deep paths #1724 done; iTunes sync n/a (ops are stubs); broad ~1363-call residual remains open -->
- [ ] **SLOG-PROD-VERIFY** Smoke-test metadata-fetch on prod to verify the full chain (opID in logs, `/api/v1/operations/:id/activity` returns rows).  <!-- 2026-07-01: ⏳ code/endpoint exist (docs/slog-prod-verify.md); remaining is a live-prod smoke-test run. -->
- [x] **CACHE-WARMUP-ROOT-CAUSE** Investigate root cause of cache warm-up OOM. Likely issue: `List*WithCounts()` allocates unboundedly during scan, or the `Server` struct cache fields retain full API response objects. Once fixed, re-enable startup preload. — ✅ verified done 2026-07-01: startup preload re-enabled (server_lifecycle.go:277); warmers rewritten to typed counts, no gin.H (entity_cache_warmers.go, commit 4515cb2c). Live OOM re-confirmation is operational.

---

## ✅ Completed — May 11, 2026

- [x] **SERVER-THIN-1** Extract `DashboardService` → `internal/sysinfo` (PR #803)
- [x] **SERVER-THIN-2** Extract `UpdateService` (config) → `internal/config` (PR #804)
- [x] **SERVER-THIN-3** Extract `MetadataStateService` → `internal/metafetch` (PR #805)
- [x] **SERVER-THIN-4** Extract `EvaluateSmartPlaylist` → `internal/playlist` (PR #807)
- [x] **SERVER-THIN-5** Fix stale Queue mock + GlobalQueue references blocking CI
- [x] **SERVER-THIN-6** Wave 2 parallel-sweep (PRs #807–#816): sweep, work, undo, batch, path-format, openlibrary, reconcile, similar-books, user-tags, maintenance
- [x] **SERVER-THIN-7** Wave 3 parallel-sweep (PRs #817–#829): scheduler, metabatch, deluge, dedup, organizer/checkpoint, backfills, covers, archive-sweep, versions, itl-rebuild, remux, import-collision, audio-sample. `internal/server` is now a pure HTTP adapter layer.

---

## 🔜 Next — Post-server-thinning

- [x] **SERVER-PLUGIN-REG** Service registry analogous to `opRegistry`. Spec + plan
  in `docs/architecture/server-plugin-registry-{design,plan}.md`. All 7 waves shipped:
  - [x] **W0** Registry foundation — `internal/serviceregistry` package + 12 tests (PR #832)
  - [x] **W1** Leaf services (PRs #835–#843)
  - [x] **W1.INT** NewServer registry-flow integration (PR #844)
  - [x] **W2** Cross-wired services — metafetch / activity / merge / quarantine / organize (PRs #864–#868)
  - [x] **W2.INT** Wire W2 services into NewServer (PR #869)
  - [x] **W3** Start/Stop services — writebackbatcher / updatescheduler / activitywriter / searchindex / opregistry / batchpoller / librarywatcher (PRs #870–#877 incl. fix-up)
  - [x] **W4** Embedding/AI cluster — embedclient / llmparser / embeddingstore / chromemstore / aijobsstore / dedup / metadatascorer / metadatallmscorer (PR #878)
  - [x] **W5** UOS plugin migrations (PR #879) — 3 real registrations (dedup, acoustid, deluge), 2 documented stubs (maintenance, itunes) blocked on server-bound closures
  - [x] **W6** Scheduler residual extraction — closes SERVER-THIN-RESIDUAL (PR #880)
  - [x] **W7** Final wrap-up (this PR) — CHANGELOG/TODO consolidated; follow-ups split out below

### 🧹 Follow-ups split from SERVER-PLUGIN-REG W7

The "trim NewServer to ≤50 lines" and "audit GetGlobalStore" deliverables from
the original W7 plan turned out to be substantially larger than a single
final-cleanup PR. Splitting them out as their own tickets to be worked
incrementally:

- [x] **SERVER-LIFECYCLE-FLIP** Wire `Container.Start(ctx)` / `Container.Stop(ctx)`
  into Server.Start / Server.Shutdown. **Completed** across PRs #882–#951:
  all sub-services (updatescheduler, searchindex, activitywriter, aiScanStore,
  pipelineManager, dedupEngine) are container-driven. Verified in code.

- [x] **SERVER-GLOBAL-STORE-AUDIT** Remove production `database.GetGlobalStore()`
  callers. **Completed**: production code has 3 remaining calls, all of which
  are intentional test-path fallbacks in `server.go:Store()`, `server.go:NewServer`,
  and `scanner.go:getStore()`. No hot-path callers remain.

- [~] **PLUGIN-DECOUPLE-SERVER-CLOSURES** Decouple `itunesservice.Service` from
  server-bound closures (`OnBookCreated`, `OrganizerFactory`). Deferred to
  post-matcher work.

  **Maintenance plugin — done (PR #935).** The empty stub registration was
  deleted; the plugin registers inline from `internal/server/server.go:~402`
  and that is the documented canonical pattern until `ServerDeps` itself is
  broken up. See `internal/plugins/maintenance/register.go` for the rationale.

  **itunesservice — ✅ done (Task 032, verified 2026-05-17).** `OnBookCreated` closure replaced by `plugin.EventPublisher` (publishes `EventBookImported`); dedup engine subscribes via EventBus in `Engine.PostInit` (`internal/dedup/lifecycle.go`). `OrganizerFactory` remains as the sole closure by design — it's a lazy factory that injects the organizer without importing internal/organizer into itunesservice. No *Server captures remain. Wiring: `internal/server/registry_wire.go:~211–245`.

- [x] **SERVER-THIN-RESIDUAL** `scheduler_extra_ops.go` residual extracted to
  `internal/scheduler/extra_ops.go` as `*ExtraOpsRegistrar` (W6). All 13 ops moved;
  server shim delegates via `s.extraOpsRegistrar`.

- [x] **SERVER-THIN-8** Pre-existing iTunes/organize/scan timeout failures fixed
  (PRs #919, #920 — 2026-05-13). Root causes: (a) test setup didn't start the
  opRegistry worker pool so enqueued ops never ran; (b) plugin SDK's
  `itunes.import` stub (Isolate=true, Run=no-op) won the registration race and
  routed runs through a no-op subprocess; (c) handler returned legacy v1 op
  id while v2 was the canonical record. Tests now Start the registry, the
  stub is removed from the plugin Register list, and the v2→v1 status
  bridge fires from `itunes_ops` and `folder_autoscan_op`.

---

## 🧹 Repo Size — Git History Bloat (1.69 GB)

- [ ] **REPO-SIZE-1** Repo is 1.69 GB — likely test fixtures committed directly to history.
  - Audit: `git rev-list --objects --all | git cat-file --batch-check='%(objecttype) %(objectname) %(objectsize) %(rest)' | sort -k3 -n -r | head -40` to find the largest blobs.
  - Plan: move test media files to an external host (GitHub Releases asset, S3, or a simple HTTP server on 172.16.2.30); have tests download on-demand via a `testdata/fetch.go` helper that caches locally and skips if `TEST_SKIP_LARGE_FIXTURES=1`.
  - Use `git filter-repo` (NOT `git filter-branch`) to rewrite history and remove the blobs. Coordinate with any forks/clones before force-push.
  - After rewrite: add the large-file extensions to `.gitignore` and optionally enable GitHub's push protection to prevent recurrence.

## 🧹 Tech Debt Sweep — Deprecated Code & Warnings

- [x] **TECHDEBT-1** Audit and remove deprecated code across the entire codebase
  - Backend: scan for `// Deprecated:` markers, dead code paths flagged in past evaluations, unused exported symbols, packages with replacement candidates already in use.
  - Frontend: ~~resolve **React Router v6 future-flag warnings**~~ (done — PR #949; `v7_startTransition` + `v7_relativeSplatPath` added to all BrowserRouter/MemoryRouter usages in main.tsx and all test files). Then upgrade-prep for v7 properly.
  - Frontend: audit `package.json` for deprecated transitive deps (`npm outdated`, `npm audit`), remove dead Material-UI v4-style imports if any remain, ~~kill `console.log` left in src~~ (verified clean — no console.log in production source files).
  - Go: `staticcheck`/`go vet` clean run; remove unused mocks; ~~replace `ioutil.*` with `io`/`os`~~ (verified clean — no `io/ioutil` imports remain); collapse redundant context plumbing flagged in `docs/codebase-evaluation.md`.
  - SQL: drop schema columns/tables marked deprecated in migration history once readers/writers are gone.
  - Tests: replace `t.Skip` markers, remove `//nolint` that no longer apply, dedupe fixture builders.
  - Output: one PR per cluster (router warnings, backend deprecated APIs, frontend deps, dead code) so each can review/revert independently.

---

## 🔒 Security Alert Sweep — Audit 2026-05-03

**Complete inventory and remediation plan for all GitHub security alerts.**

**Audit Documents:**
- **Spec:** [`docs/security/audit-2026-05-03/spec.md`](docs/security/audit-2026-05-03/spec.md) — Alert inventory, severity breakdown, remediation recommendations
- **Implementation Plan:** [`docs/security/audit-2026-05-03/implementation-plan.md`](docs/security/audit-2026-05-03/implementation-plan.md) — Phased remediation plan (11 phases, 16 tasks, ~44 hours)
- **Raw Data:** [`docs/security/audit-2026-05-03/raw/`](docs/security/audit-2026-05-03/raw/) — JSON dumps from `gh api`

**Alert Totals (as of 2026-05-03):**
- **Code Scanning:** 602 total (235 open, 17 dismissed, 350 fixed)
- **Dependabot:** 20 total (1 open, 19 fixed)
- **Secret Scanning:** 0 alerts

**Open Alert Breakdown (236 total):**
- **231 Error/High:** 217 path injection, 6 clear-text logging, 4 SSRF, 2 allocation, 1 zipslip, 1 weak hashing
- **5 Warning/Medium:** 4 code scanning warnings, 1 Dependabot (follow-redirects)

### Phase -1: CodeQL Custom Sanitizer Pack (Noise Reduction)

- [x] **SEC-AUDIT--1** Deploy CodeQL Models-as-Data pack for existing sanitizers
  - **Priority:** P0 (Unblocks Phase 1-6 by reducing false positives)
  - **Effort:** 2 hours
  - **Alerts:** Expected to reduce path injection from 217 → ~120-140 (~77 FP reduction)
  - **Files:** `.github/codeql/` (new pack), `.github/workflows/codeql.yml`, `docs/security/audit-2026-05-03/spec.md`
  - **Action:** Create MaD pack declaring `internal/util.SafeJoin` and `internal/util.WithinRoot` as path-injection sanitizers
  - **Dependencies:** None
  - **Status:** ✅ **IN PROGRESS** (PR pending)
  - **Details:** 
    - Pack declares `SafeJoin` return value as barrier for path-injection
    - Pack declares `WithinRoot` as barrier guard (conditional sanitizer)
    - Based on sast-sca-auditor spot-check: 35-45% of alerts are FPs from CodeQL not recognizing existing sanitizers
  - **Spec:** [`spec.md#remediation-strategy-phase-0`](docs/security/audit-2026-05-03/spec.md#remediation-strategy)

### Phase 0: Unblock Govulncheck

- [x] **SEC-AUDIT-0** (PR #1012) Enable govulncheck for `GOEXPERIMENT=jsonv2` builds
  - **Priority:** P0 (Blocker)
  - **Effort:** 1 hour
  - **Alerts:** N/A (unblocks Go vuln detection)
  - **Files:** `.github/workflows/vulnerability-scan.yml`
  - **Action:** Switch to binary-mode scanning (`govulncheck -mode=binary`)
  - **Dependencies:** None
  - **Spec:** [`spec.md#govulncheck-blocker`](docs/security/audit-2026-05-03/spec.md#govulncheck-blocker--goexperimentjsonv2)
  - **Plan:** [`implementation-plan.md#phase-0`](docs/security/audit-2026-05-03/implementation-plan.md#phase-0-enable-govulncheck-unblock-vulnerability-scanning)

### Phase 1-6: Path Injection (217 alerts)

- [x] **SEC-AUDIT-1** Create `internal/security/pathvalidation` package (foundation)
  - **Priority:** P0
  - **Effort:** 4 hours
  - **Alerts:** Foundation for 217 path injection alerts
  - **Files:** `internal/security/pathvalidation/` (new)
  - **Action:** Build centralized path validation utilities (`ValidateRelativePath`, `SanitizeFilename`, `SecureJoin`)
  - **Dependencies:** Phase 0
  - **Plan:** [`implementation-plan.md#phase-1`](docs/security/audit-2026-05-03/implementation-plan.md#phase-1-path-injection--foundation-build-validation-utilities)

- [x] **SEC-AUDIT-2** Fix path injection in fileops layer (9 alerts: #625-#620, #543, #542, #539, #538-#536)
  - **Priority:** P0
  - **Effort:** 6 hours
  - **Files:** `internal/fileops/` (service.go, hash.go, write_tags_safe.go, safe_operations.go)
  - **Dependencies:** Phase 1
  - **Plan:** [`implementation-plan.md#phase-2`](docs/security/audit-2026-05-03/implementation-plan.md#phase-2-path-injection--apply-validation-file-operations-core)

- [x] **SEC-AUDIT-3** Fix path injection in cover handlers (9 alerts: #602-#594) — PR #1015
  - **Priority:** P0
  - **Effort:** 3 hours
  - **Files:** `internal/server/covers.go`, `internal/server/cover_history.go`
  - **Dependencies:** Phase 2
  - **Plan:** [`implementation-plan.md#phase-3`](docs/security/audit-2026-05-03/implementation-plan.md#phase-3-path-injection--server-handlers-covers)

- [x] **SEC-AUDIT-4** Fix path injection in iTunes/transfer/audiobook handlers (20+ alerts: #627-#603, #619-#588) — PR #1016
  - **Priority:** P0
  - **Effort:** 6 hours
  - **Files:** `internal/server/itunes_handlers.go`, `internal/itunes/service/transfer.go`, `internal/server/audiobooks_handlers.go`, `internal/audiobooks/service.go`, `internal/server/server.go`
  - **Dependencies:** Phase 2
  - **Plan:** [`implementation-plan.md#phase-4`](docs/security/audit-2026-05-03/implementation-plan.md#phase-4-path-injection--itunestransferserver-core)

- [x] **SEC-AUDIT-5** Fix path injection in scanner/reconcile/OpenLibrary (15+ alerts: #618-#608)
  - **Priority:** P0
  - **Effort:** 5 hours
  - **Files:** `internal/scanner/service.go`, `internal/reconcile/reconcile.go`, `internal/server/openlibrary_service.go`, `internal/importer/service.go`
  - **Dependencies:** Phase 2
  - **Plan:** [`implementation-plan.md#phase-5`](docs/security/audit-2026-05-03/implementation-plan.md#phase-5-path-injection--scannerreconcileopenlibrary)

- [x] **SEC-AUDIT-6** Fix path injection in backup/Deluge/remaining (10+ alerts: #541, #535-#534, others) — PR #1018
  - **Priority:** P0
  - **Effort:** 3 hours
  - **Files:** `internal/backup/backup.go`, `internal/server/deluge_import_unix.go`
  - **Dependencies:** Phase 2
  - **Plan:** [`implementation-plan.md#phase-6`](docs/security/audit-2026-05-03/implementation-plan.md#phase-6-path-injection--backupdelugeremaining)

### Phase 7: Non-Path-Injection Errors (14 alerts)

- [x] **SEC-AUDIT-7a** Fix clear-text logging — Converted all `log.Printf` in `maintenance_fixups.go` to structured `slog.Info`/`slog.Warn` with named key-value attrs (PR #957). `cmd/root.go` uses `fmt.Printf` for CLI output, not a logging sink — no change needed (false positive).

- [x] **SEC-AUDIT-7b** Fix SSRF in `DownloadCoverArt` — Added `safeCoverDialContext` with RFC1918/loopback/link-local IP blocking and scheme validation (`http`/`https` only) to `metadata/cover.go` (PR #958). `server/covers.go` already had `IsAllowedCoverSource` domain allowlist. `deluge/client.go` + `plugins/webhook/plugin.go` connect to admin-configured endpoints (not SSRF-relevant).

- [x] **SEC-AUDIT-7c** Fix uncontrolled allocation — `MaxScanBufferBytes` cap added to `scanner.go` in PR #768; buffer capped at `hashChunkSize` (1 MiB).

- [x] **SEC-AUDIT-7d** Fix zipslip in backup extraction — `isPathWithinTarget` already implemented in `backup/backup.go` with `filepath.Rel` escape check; called before every tar entry extraction.

- [x] **SEC-AUDIT-7e** Fix weak sensitive data hashing — `settings.go` already uses `argon2.IDKey` (Argon2id, 64 MiB, 1 iter, 4 threads) via `DeriveKeyFromPassword`. SHA-256 replaced in a prior PR.

### Phase 8: Warnings (4 alerts)

- [x] **SEC-AUDIT-8** Fix warning-level alerts (4 alerts: #379, #468, #160, #50) — PR #970
  - **Priority:** P2-P3
  - **Effort:** 3.5 hours
  - **Alerts:** Disabled cert check (#379), allocation overflow (#468), JS cert bypass (#160), incomplete sanitization (#50)
  - **Files:** `internal/mtls/provisioning.go`, `internal/itunes/itl.go`, `scripts/record_demo.js`, `web/src/pages/Settings.tsx`
  - **Plan:** [`implementation-plan.md#phase-8`](docs/security/audit-2026-05-03/implementation-plan.md#phase-8-warnings-4-alerts)

### Phase 9: Dependabot

- [x] **SEC-AUDIT-9** Bump follow-redirects to 1.16.0+ (1 alert: #27, GHSA-r4q5-vmmm-2653)
  - **Priority:** P2
  - **Effort:** 0.5 hours
  - **Files:** `web/package-lock.json`
  - **Action:** `npm update follow-redirects && npm audit fix`
  - **Plan:** [`implementation-plan.md#phase-9`](docs/security/audit-2026-05-03/implementation-plan.md#phase-9-dependabot-1-alert)

### Phase 10: Documentation

- [x] **SEC-AUDIT-10** Document path validation policy & add dismissal comments
  - **Priority:** P3
  - **Effort:** 1.5 hours
  - **Action:** Create `docs/security/path-validation-policy.md`, add comments to 13 dismissed alerts (#560-#547)
  - **Plan:** [`implementation-plan.md#phase-10`](docs/security/audit-2026-05-03/implementation-plan.md#phase-10-documentation--dismissed-alerts)

### Phase 11: Verification

- [ ] **SEC-AUDIT-11** Final verification — Dismiss post-audit findings  <!-- 2026-07-01: ⏳ closeout doc exists (sec-audit-11-closeout.md); actual CodeQL alert dismissal is a GitHub-console action. -->
  - **Current Status:** 492 open alerts (mostly post-audit findings, not in scope of Phases 1-10)
  - **Breakdown:**
    - `go/path-injection` 220 (217 original + 3-9 new from May 18 code; new ones from OTEL/legacy-migration likely safe)
    - `go/log-injection` 255 (new category post-audit; CodeQL conservatively flags %s format usage; likely 90%+ false positives)
    - Other 17 (request-forgery 4, allocation 2, workflow perms 2, others 9)
  - **Action:** Re-run codescanning alerts query and document findings. Original Phases 1-10 successfully remediated 217 path-injection and 6 clear-text logging alerts. Post-audit findings (log-injection, +9 path-injection) represent new CodeQL pattern maturity or code changes, not regressions. Recommend dismissing as accepted-risk with documented rationale per alert.
  - **Success Criteria:** All original 236 alerts (Phase 0-10 scope) have been addressed. New post-audit findings to be scoped separately (Phase 12).
  - **Completion:** Mark Phase 11 complete once bulk-dismissal rationales are added to CodeQL dashboard

### Phase 12: Log Injection (255 alerts, NEW category post-audit)

- [x] **SEC-AUDIT-12** Investigate and remediate log+path injection alerts — **DONE 2026-06-17**. Current counts were 14 log-injection + 73 path-injection (the "255" was stale). Remediation: runtime sanitizer `logger.sanitizeLogLine` at all log sinks (PR #1490); import-path scan guard (#1491); membership-gate sweep on exclusion/import/ingest/update (#1492); relocate gate + `/etc/audiobook-organizer` package prefix (#1494); iTunes treated as accepted-risk (admin-only, single config file). All 87 path+log CodeQL alerts dismissed with per-category rationale (guarded-at-runtime / sanitized / accepted-risk / triaged false-positive). **`go/path-injection` and `go/log-injection` open counts are now 0.** Note: the original "%s is safe" assessment below was wrong for log-injection (CR/LF in user input forges log lines regardless of `%s`); the sanitizer addresses it.
  - _(original notes retained for history)_ [hold] Investigate and remediate log-injection alerts
  - **Priority:** P1 (review required; likely low-risk false positives)
  - **Alerts:** 255 open (go/log-injection, error severity)
  - **Affected areas:** dedup, server handlers, system services (all files logging bookID, userInput, or paths)
  - **Root cause analysis:** CodeQL flags user-controlled data (bookID, file paths, book IDs) flowing into `log.Printf(...%s...)` calls. With %s format specifier, this is safe — input is interpolated as literal string, not executed as format string.
  - **Distinction from clear-text logging:** This is not about sensitive data visibility; it's about format-string injection risk in logging APIs.
  - **Assessment:** Likely 90%+ false positives with standard `log.Printf`/`fmt.Errorf` using %s. Remediation (if needed) involves wrapping user data with safe logging helpers or structured slog attributes.
  - **Decision point:** Recommended approach is to dismiss with rationale: "Log injection with %s format specifier is safe; user input is interpolated as literal string, not executed." Alternatively, create slog structured logging migration for higher confidence.
  - **Effort if fixing:** 8-12 hours to audit all 255 occurrences and apply structured logging.

**Estimated Total Effort (Phases 1-11 COMPLETE + Phase 12 optional):** 44 hours base + 8-12 hours optional Phase 12 remediation

**Acceptance Criteria:**
- ✅ All 236 open alerts addressed (fixed or consciously dismissed with rationale)
- ✅ Govulncheck runs successfully on jsonv2 builds
- ✅ All PRs merged, `make ci` passes on main
- ✅ Post-remediation audit confirms 0 open alerts (or only accepted-risk)

---

## 📊 Codebase Evaluation — 2026-04-30

Full evaluation of the audiobook-organizer backend and frontend. 12 issue groups,
38 atomic bot-task PRs. Specs: `docs/superpowers/specs/2026-04-30-*.md`.
Bot-tasks: `docs/superpowers/bot-tasks/2026-04-30-*.md`.

### MOCK — Mock/CI Gate (2 tasks)

- [x] **MOCK-1** `fix/regenerate-mocks` — Mocks verified fresh via `mockery` run; no diff.
- [x] **MOCK-2** `fix/mock-ci-gate` — CI gate already in `.github/workflows/ci.yml` (`mocks-check` job).

### N1 — N+1 Query Elimination (4 tasks)

- [x] **N1-1** `perf/batch-fetch-interface` — Add batch-fetch methods to Store interface (`GetAuthorsByIDs`, `GetSeriesByIDs` added to AuthorReader/SeriesReader)
- [~] **N1-2** `perf/n1-sqlite-impl` — SQLiteStore loop impl added for interface conformance (SQLite is legacy-only, no prod path)
- [x] **N1-3** `perf/n1-pebble-impl` — PebbleStore `GetAuthorsByIDs` + `GetSeriesByIDs` implemented
- [x] **N1-4** `perf/n1-enrich-response` — `EnrichAudiobooksWithNames` rewired to collect→batch→hydrate (PR #955)

### SEC — Filesystem / Security (4 tasks)

- [x] **SEC-1** `fix/browse-dir-allowlist` — Done: `isAllowedPath` check in `fileops/service.go:BrowseDirectory`; returns `ErrPathNotAllowed`.
- [x] **SEC-2** `fix/auth-enabled-default` — Done: `[WARN] authentication is disabled` log in `server_lifecycle.go:851`.
- [x] **SEC-3** `fix/rate-limit-default` — Done: `[WARN] rate limiting is disabled` log in `server_lifecycle.go:854`.
- [x] **SEC-4** `fix/ratelimit-o1-cleanup` — No duplicate found; single `IPRateLimiter` in `server/middleware/ratelimit.go`, applied once in `server_lifecycle.go:859`.

### DB — Database Hygiene (6 tasks)

- [~] **DB-1** `fix/db-file-hash-index` — SQLite-only; deferred until SQLite elimination.
- [~] **DB-2** `fix/db-begin-tx-sqlite` — SQLite-only; deferred until SQLite elimination.
- [~] **DB-3** `fix/db-begin-tx-activity` — SQLite/NutsDB; deferred pending NutsDB evaluation.
- [x] **DB-4** `fix/pipeline-save-errors` — `acoustid_backfill.go` errors already propagated;
  `server_lifecycle.go` discards are intentional best-effort (verified).
- [~] **DB-5** `fix/db-time-parse-errors` — SQLite-only; deferred until SQLite elimination.
- [x] **DB-6** `fix/pebble-silent-errors` — Added `slog.Warn` to `RecordPathChange` on
  book create and `recomputeDurationMap` on segment create in `pebble_store.go`.

### CTX — Context Propagation (3 tasks)

- [x] **CTX-1** `fix/ctx-audiobook-update-service` — Done: `AudiobookUpdateService.UpdateAudiobook` already accepts `ctx context.Context` and threads it to `audiobookService.UpdateAudiobook`.
- [x] **CTX-2** `fix/ctx-openlibrary-service` — Done: all `OpenLibraryClient` methods (`SearchByTitle`, `SearchByTitleAndAuthor`, `GetBookByISBN`) already accept `ctx context.Context`.
- [x] **CTX-3** `fix/ctx-filesystem-handlers` — Added `ctx context.Context` to `BrowseDirectory`, `CreateExclusion`, `RemoveExclusion`; handlers pass `c.Request.Context()` (PR #956).

### LOG — Structured Logging (4 tasks)

- [x] **LOG-1** `fix/log-tagger-structured` — Done: `tagger/tagger.go` and `tagger/safe_write.go` converted to `slog`.
- [x] **LOG-2** `fix/log-fileops-structured` — Done: no `log.Printf` in `internal/fileops`.
- [x] **LOG-3** `fix/log-backup-structured` — Done: `backup/backup.go` converted to `slog`.
- [x] **LOG-4** `fix/scanner-remove-progressbar` — Done: no progress bar in scanner; `chapter_consolidation.go` converted to `slog`.

### PROJ — Query Projection (2 tasks)

- [x] **PROJ-1** `perf/book-summary-columns` — Done: `BookSummary` struct defined in `internal/database/store.go:269`; SQLite projected query uses `bookSummarySelectColumns` (excludes description, embeddings, heavy fields).
  → [`2026-04-30-proj-1-summary-columns.md`](docs/superpowers/bot-tasks/2026-04-30-proj-1-summary-columns.md)
- [x] **PROJ-2** `perf/book-list-summary-query` — Done: `GetAllBookSummaries` implemented in both `PebbleStore` and `SQLiteStore`; audiobooks service uses it for the default library list path.
  → [`2026-04-30-proj-2-list-query.md`](docs/superpowers/bot-tasks/2026-04-30-proj-2-list-query.md)

### SCAN — Scanner Efficiency (1 task)

- [x] **SCAN-1** `perf/scanner-walkdir` — Replace filepath.Walk with filepath.WalkDir
  → [`2026-04-30-scan-1-walkdir.md`](docs/superpowers/bot-tasks/2026-04-30-scan-1-walkdir.md)

### SRV — Server Response Optimization (2 tasks)

- [x] **SRV-1** `feat/server-gzip-compression` — Done: `gzip.Gzip(DefaultCompression)` middleware wired in `server.go` (excludes `/api/events`).
- [x] **SRV-2** `fix/sse-heartbeat` — Done: `fmt.Fprintf(c.Writer, ": heartbeat\n\n")` in `operations_v2_handlers.go:237`.

### FE — Frontend Cleanup (10 tasks)

- [x] **FE-1** `refactor/library-filter-panel` — Done: `useLibraryFilters` hook created (`web/src/hooks/useLibraryFilters.ts`); moves filter state, available-data loading, and handlers out of `Library.tsx`.
  → [`2026-04-30-fe-1-filter-panel.md`](docs/superpowers/bot-tasks/2026-04-30-fe-1-filter-panel.md)
- [x] **FE-2** `refactor/library-book-grid` — Done: `LibraryBookGrid.tsx` extracted.
- [x] **FE-3** `refactor/library-batch-toolbar` — Done: `LibraryToolbar.tsx` extracted.
- [x] **FE-4** `refactor/settings-general-tab` — Done: `web/src/components/SettingsGeneral.tsx` exists and is imported in `Settings.tsx`.
  → [`2026-04-30-fe-4-settings-general.md`](docs/superpowers/bot-tasks/2026-04-30-fe-4-settings-general.md)
- [x] **FE-5** `refactor/settings-paths-tab` — Done: `PathsSettingsTab.tsx` extracted.
- [x] **FE-6** `refactor/settings-metadata-tab` — Done: `MetadataSettingsTab.tsx` extracted.
- [x] **FE-6 (audit)** `refactor/fe6-settings-handlers-split` — Done: `useSettingsHandlers.ts` (1259→936 lines) split into `useImportFolderHandlers`, `useBackupHandlers`, `useMetadataSourceHandlers`.
- [x] **FE-7** `fix/frontend-remove-console-logs` — Done: no `console.log` calls in production source; only `console.error`/`console.warn` in catch blocks (appropriate).
- [x] **FE-8** `fix/frontend-error-boundaries` — Done: `ErrorBoundary` wraps every page route in `App.tsx`.
- [x] **FE-8 (audit)** `fix/fe8-real-server-smoke-test` — Done: real-server auth smoke test added to `auth-flow.spec.ts`; exercises first-run bootstrap + real session cookie against live webServer.
- [x] **FE-9** `fix/frontend-localstorage-keys` — Done: `STORAGE_KEYS` constants exported from `lib/storageKeys.ts`.
- [x] **FE-10** [hold] `chore/frontend-coverage-thresholds` — Add Vitest coverage thresholds — ✅ verified done 2026-07-01: vitest.config.ts:22 thresholds {lines:30,functions:20,branches:20,statements:25}
  → [`2026-04-30-fe-10-coverage.md`](docs/superpowers/bot-tasks/2026-04-30-fe-10-coverage.md)

### STRUCT — Structural Refactors — 2026-05-01

Full audit at [`docs/audits/2026-05-01-structure-audit.md`](docs/audits/2026-05-01-structure-audit.md).
Bot-tasks at [`docs/superpowers/bot-tasks/2026-05-01-struct-*.md`](docs/superpowers/bot-tasks/).

- [x] **STRUCT-1** — Migrate all direct `c.JSON` calls to `httputil.RespondWith*` helpers
  → [`2026-05-01-struct-1-server-response-helpers.md`](docs/superpowers/bot-tasks/2026-05-01-struct-1-server-response-helpers.md)
  ✅ `internal/httputil/` created; 0 raw `c.JSON` calls remain outside test files
- [x] **STRUCT-2** — Consolidate duplicate pagination parsers into `httputil.ParsePaginationParams`
  → [`2026-05-01-struct-2-pagination-helper.md`](docs/superpowers/bot-tasks/2026-05-01-struct-2-pagination-helper.md)
  ✅ `internal/httputil/parse.go` exports `ParsePaginationParams`; `server/pagination.go` deleted
- [x] **STRUCT-3** — Reduce 6400-line `maintenance_fixups.go`
  → [`2026-05-01-struct-3-maintenance-fixups-split.md`](docs/superpowers/bot-tasks/2026-05-01-struct-3-maintenance-fixups-split.md)
  ✅ ASYNC-CLEAN-1 removed old sync maintenance handlers; file reduced 6400→581 lines; 8-domain split no longer necessary
- [x] **STRUCT-4** — Split 3932-line `metafetch/service.go` into domain files
  → [`2026-05-01-struct-4-metafetch-service-split.md`](docs/superpowers/bot-tasks/2026-05-01-struct-4-metafetch-service-split.md)
  ✅ Split into 11 files: `service_writeback.go`, `service_apply.go`, `service_scoring.go`, `service_search.go`, `service_fetch.go`, `service_normalize.go`, `service_files.go`, `helpers.go`, `isbn.go`, `file_pipeline.go`, `path_format.go`
- [x] **STRUCT-5** — Extract shared `withRetry` helper from 4 identical AI retry loops
  → [`2026-05-01-struct-5-ai-retry-helper.md`](docs/superpowers/bot-tasks/2026-05-01-struct-5-ai-retry-helper.md)
  ✅ `internal/ai/retry.go` created; wired into 5 AI callers
- [x] **STRUCT-6** — Split 6976-line `sqlite_store.go` into 7 domain files
  → [`2026-05-01-struct-6-sqlite-store-split.md`](docs/superpowers/bot-tasks/2026-05-01-struct-6-sqlite-store-split.md)
  ✅ `sqlite_store.go` deleted; 7 domain files created under `internal/database/`
- [x] **STRUCT-7** — Split 3401-line `server.go` into 6 responsibility files
  → [`2026-05-01-struct-7-server-go-split.md`](docs/superpowers/bot-tasks/2026-05-01-struct-7-server-go-split.md)
  ✅ `server.go` reduced to 853 lines; 6 split files created
- [x] **STRUCT-8** — Extract `useAsyncAction` hook from 148 `setLoading` patterns
  → [`2026-05-01-struct-8-use-async-action-hook.md`](docs/superpowers/bot-tasks/2026-05-01-struct-8-use-async-action-hook.md)
  ✅ `web/src/hooks/useAsyncAction.ts` created and wired
- [x] **STRUCT-9** — Split oversized frontend page components into sub-components *(completed)*
  → [`2026-05-01-struct-9-frontend-component-splits.md`](docs/superpowers/bot-tasks/2026-05-01-struct-9-frontend-component-splits.md)
  ✅ `Library.tsx` reduced 3243 → 1916 lines (LibraryToolbar, LibraryBookGrid, LibraryDialogs extracted)
  ✅ `BookDedup.tsx` reduced 3424 → 1656 lines (DedupAdvancedScanTab, DedupAuthorTab, DedupSeriesTab, DedupReconcileTab extracted)
  ✅ `BookDetail.tsx` reduced 2773 → 1073 lines (BookDetailHeader, BookDetailActions, BookDetailInfoTab, BookDetailFilesTab, BookDetailDialogs, BookDetailVersionGroup, BookDetailStatusAlerts extracted)
- [x] **STRUCT-10** — Narrow `*Server` receivers with small local interfaces in handler groups *(completed)*
  → [`2026-05-01-struct-10-narrow-server-interfaces.md`](docs/superpowers/bot-tasks/2026-05-01-struct-10-narrow-server-interfaces.md)
  ✅ `internal/server/interfaces.go` with 4 narrow store interfaces + compile-time assertions
  ✅ Handler receivers narrowed in organize_handlers.go, ai_jobs_handlers.go, filesystem_handlers.go, reading_handlers.go, activity_handlers.go

#### STRUCT — Open gaps from audit (no task yet)

- [x] **STRUCT-11** — Split 1686-line `scheduler.go` into domain files *(completed)*
  ✅ scheduler_core.go (254 lines), scheduler_tasks.go (1101 lines), scheduler_triggers.go (69 lines), scheduler_maintenance.go (344 lines)
- [x] **STRUCT-12** — Create `internal/util/normalize.go` path/string normalization helper *(completed)*
  ✅ NormalizePath, NormalizeTitle, NormalizeAuthor, NormalizeString, CollapseSpaces; 45 call-chain replacements across 5 files
- [x] **STRUCT-13** — Finish splitting `BookDetail.tsx` (2773 lines) into sub-components *(completed)*
  ✅ See STRUCT-9 above — BookDetail.tsx reduced to 1073 lines

#### ARCH — Architecture (June 2026 audit)

- [x] **ARCH-3** — Unified op-launch helpers: `launchOp` (operations handler) and `launchLegacyOp` (duplicates handler) shipped PR #1577. Eliminates enqueue boilerplate across 11 callers.
- [x] **ARCH-4** — Work-item contract: `RunItems[T]` standalone generic function + `ErrMode`/`RunItemsOptions` + 9 unit tests shipped PR #1579. Eliminates ctx.Done/UpdateProgress/SetCurrentItem boilerplate in new fan-out ops.
- [x] **ARCH-4 (remap centralization)** — Config remap machinery: 6 per-group `remap*Keys` functions in `update_service.go` replaced with `configRemapGroups` table + generic `applyLegacyRemaps`. Single source of truth — adding new legacy-key groups requires one entry, not a new function. PR #1594.
- [x] **ARCH-4b (wave 1)** — `deluge/path_update.go` migrated to `registry.RunItems[database.BookVersion]` with `ErrModeCollect`. `updated` counter via `atomic.Int64`. PR #1591.
- [x] **ARCH-4b (wave 2)** — `deluge/centralization.go` migrated: pre-sliced to checkpoint.ProcessedFiles, atomic counters (success/skip/err), checkpoint written inside RunItems fn closure, IsCanceled() replaced by ctx-based RunItems polling. PR #1592.
- [x] **ARCH-7** — Compatibility surface registry: `docs/compat-surfaces.md` documents 8 shim files (server→organizer forwarding layers, audiobooks→organizer aliases, deprecated config/logger surfaces) with re-export targets and removal conditions. PR #1608.
- [x] **ARCH-8** — Typed service keys: added `internal/serviceregistry/keys.go` with 24 constants (`KeyStore`, `KeyConfig`, `KeyActivity`, etc.). Replaced 68 string literals in `Get[T]`, `Name:`, and `Needs:` across 25 `register.go` files. PR #1607.
- [x] **ARCH-4b (wave 3)** — Remaining 3 sites: `lsh_backfill.go` (308K-item progress cadence — needs reporter throttle wrapper before RunItems is appropriate), `acoustid/backfill.go` (nested books→files loop + resume-by-book-ID — requires flat-map preprocessing), `acoustid/reset_all.go` (callback-driven PebbleStore.ClearAllAcoustIDFingerprints API + dual heterogeneous loops). `acoustid/fingerprint_rescan.go` excluded — already uses semaphore goroutine pool that outperforms sequential RunItems.  <!-- 2026-07-01: ◑ PARTIAL: lsh_backfill.go + acoustid/backfill.go migrated to RunItems; acoustid/reset_all.go remains. -->  ✅ shipped #1716 (agent-task sweep 2026-07-01)
- [x] **PERF-2** — Batch upserts in `createBookFilesForBook`: N per-segment `UpsertBookFile` calls replaced with one `BatchUpsertBookFiles` call (shipped PR #1583). N→1 DB writes per book on first scan.
- [x] **PERF-6** — Search index backfill cursor: added `GetAllBooksFrom(afterID, limit)` to `BookReader` interface + PebbleStore (O(1) LowerBound seek). Rewrote `server_search.go` backfill loop to use cursor pagination instead of O(offset) `GetAllBooks`. Updated 1 hand-written + 6 mockery-generated mocks. PR #1601.
- [x] **PERF-2b** — Hash carry-forward: added `Book.SegmentHashes map[string]string`; `saveBookToDatabase` dedup loop writes computed hashes back; `createBookFilesForBook` accepts `knownHashes ...map[string]string` variadic (no signature change to `saveBook` function variable); call site at line 850 passes `books[idx].SegmentHashes`. PR #1605.
- [x] **ARCH-6** — Optional store capabilities discovered ad hoc: `database.GetOpsV2(store)` + `database.GetAIJobs(store)` added to `internal/database/storecap.go`. Both walk the `Unwrap()` decorator chain (mirrors `errors.As`). 5 ad-hoc type-assertion sites replaced; `UnwrapAIJobsStore` in `handlers/ai.go` delegates to `GetAIJobs` for backward compat. PR #1606.
- [x] **PERF-5 (partial)** — iTunes backfill bulk writes: per-row `CreateExternalIDMapping` calls replaced with per-page batch accumulation + `BulkCreateExternalIDMappings` (N→1 per 10K-book page). N+1 `GetBookFiles` per book still present — deferred until `GetBookFilesByBookIDs([]string)` batch method added to Store interface (TODO comment at `itunes/backfill.go:77`).
- [x] **PERF-4** — iTunes search `SearchBooks(search, 0, 0)` returned zero results: `limit=0` was checked as `len(filtered) < 0` (always false). Fixed in `pebble_store.go:3169` — `limit==0` now means "no limit". Regression test `TestSearchBooksUnlimited` added.
- [x] **PERF-3** — Library list full-materialization escape hatches: removed non-title sort and fingerprint/coverage early-returns from `buildBookSummaryFilterWithLookupCount`. Both now push predicates into `BookSummaryFilter`; non-title sorts fetch all filtered books (not 68K unfiltered), letting the service sort+paginate in-memory on the smaller set. PR #1604.
- [x] **PERF-8** — Backup consistency: added `PebbleStore.Checkpoint(destDir)` + `backup.Checkpointable` interface + `backup.CreateBackupWithCheckpoint`. `POST /backup/create` type-asserts the live store to `Checkpointable`; PebbleStore takes the consistent Pebble snapshot path, mocks fall back to the live-walk path. No Store interface change (zero mock propagation). PR #1603.
- [x] **PERF-7** — `UpsertBookFile` memdb round-trip fingerprint data-loss: same preserve-on-empty guard as `BatchUpsertBookFiles` now applied. 3 regression tests in `pebble_bookfile_preserve_test.go`. Shipped PR (audit-remediation-p1).
- [x] **SEC-7** — `/cache/stats` + `/cache/stats/history` moved to `protected` group (PermLibraryView). `/metrics` stays accepted-risk per MED-1. Shipped PR (audit-remediation-p1).
- [x] **SEC-2-AUD** — `WriteStartupReadOnlyKey bool` config flag (default true). Operators can set `write_startup_readonly_key: false` to suppress `.readonly-key` file creation. Bootstrap token unaffected. Shipped PR (audit-remediation-p1).
- [x] **SEC-8** — Docker mutable base tags: `node:26-alpine`, `golang:1.26-alpine`, `alpine:3.24` pinned to manifest-list SHA digests in `Dockerfile` + `Dockerfile.build-cgo`. Refresh with `docker buildx imagetools inspect <image> --format '{{.Manifest.Digest}}'`.
- [x] **SEC-5** — Restore target is arbitrary absolute path + `verify=true` is a no-op: `IsDangerousRoot` check added on `target_path`; `verify=true` logs a visible warning. Shipped PR #1584.
- [x] **SEC-6** — Factory reset uses `RootDir` without validation: `IsDangerousRoot` check added before `os.RemoveAll` loop; returns 400 + logs error if RootDir is a protected system path. Shipped PR #1584.
- [x] **FE-5 (audit)** — `Library.tsx` 2018→1811 lines: extracted `useLibraryQuery` (229 lines, book-fetch state + effects + auto-refresh) and `useLibrarySelection` (164 lines, selection state + handlers). Shipped PR #1585.
- [x] **STR-4** — `BookDedup.tsx` 2907→145 lines: extracted `DedupAIReviewTab` (386 lines), `DedupEmbeddingTab` (1441 lines, embedding dedup with module-level cache + buildClusters), `DedupAcousticTab` (996 lines, acoustic dedup + `AcousticBookCard`/`AcousticBookMetadata` re-exports). Shipped PR #1585. TypeScript: 0 errors.
- [x] **TOOL-1-AUD** — Large corpus LFS: already configured in `.gitattributes` (48 LFS objects, 1.7 GB). Fixed `mediainfo_test.go:889` hardcoded absolute path → `findRepoRootForMediainfo()` inline walk. Skip message updated to guide `git lfs pull`. Shipped PR #1586.
- [x] **TOOL-3-AUD** — Demo recording isolated from default E2E: `chromium`/`webkit` projects now have `testIgnore: ['**/demo-*.spec.ts', '**/interactive-*.spec.ts']`; `chromium-record` is opt-in. `npm run test:e2e:demo` + `make test-e2e-demo` added.
- [x] **STR-3** — DB index key normalization: `pebble_store.go` author/series/alias/role/playlist/title index keys replaced `strings.ToLower(name)` (no TrimSpace) with `util.NormalizeAuthor`/`NormalizeTitle`/`NormalizeString`. Names with leading/trailing whitespace (XML import, API) now produce consistent keys on write and read. Also adopted in `memdb_indexers.go` (titleSortIndex) and `metadata_fetch_cache.go`. 49 remaining inline patterns in other packages are style (already correct logic) — incremental adoption ongoing. Shipped PR #1600.
- [x] **TOOL-7** — Fixed sleeps in E2E tests: `waitForTimeout(1000)` replaced with `waitForRequest(url)` in `dedup-operations.spec.ts` and `dedup.spec.ts`. `dynamic-ui-interactions.spec.ts` mock-handler delays retained (intentional latency simulation). Go backend `time.Sleep` calls are timing-sensitive and correct.
- [x] **TOOL-8** — Manual smoke scripts wrapped as Makefile targets: `make manual-smoke`, `make smoke-create-books`, `make smoke-run-demo`.
- [x] **TOOL-5-AUD** — Test-double style guidance: added "Prefer narrow hand-written fakes for new code" section to `docs/CODING_STANDARDS.md` Go Testing guidelines. PR #1609.
- [x] **ARCH-2** — `wire_handlers.go` split: extracted 304 route registrations into 9 per-domain `wire_*_routes.go` files. `wire_handlers.go` reduced from 978 → 414 lines. PR #1610.
- [x] **ARCH-5** — `AudiobookService` god service split: `service.go` (2691 lines) → `service_types.go`, `service_filtering.go`, `service_query.go`, `service_single.go`, `service_mutation.go`, `service_tags.go`. Core `service.go` now 171 lines. PR #1611.
- [x] **ARCH-7** — Backward-compatibility surface registry: `docs/compat-surfaces.md` catalogues all 8+ shim files with removal conditions. PR #1608.
- [x] **ARCH-1** — Handler lazy-provider coupling: removed `getStore func()` from audiobooks, metadata, dedup, duplicates handlers (54 call sites → direct field). `system.getStore` and `*.getWriteBack` kept lazy where genuinely needed. PR #1613.

---

## 🔧 CI / Release Infrastructure — Complete

- [x] Revert corrupted `release-go-action/action.yml`
- [x] `ghcommon/scripts/setup-ci-app.sh` — one-shot GitHub App creator + secret distributor
- [x] `ghcommon/reusable-release.yml` — stale draft + superseded-RC auto-cleanup on stable cuts
- [x] `ghcommon/reusable-release.yml` — keep-5 most-recent RCs policy (`RC_KEEP_COUNT`)
- [x] Create `jdfalk-ci-bot` GitHub App — done, secrets `CI_APP_ID` + `CI_APP_PRIVATE_KEY` present
- [x] Distribute secrets to repos — confirmed present on audiobook-organizer
- [x] Install App on target repos — working (releases use it for tag push)
- [x] `release-go-action/action.yml` — `github-token` input wired
- [x] `gha-release-go` — passes token through
- [x] `ghcommon/reusable-release.yml` — `create-github-app-token` wired
- [x] v0.207.0 through v0.213.0 all released successfully

---

## ⭐ User Ratings UI — DB + schema done, API + UI pending

PR #516 added full Audible rating dimensions (5 dims + count + reviews) and Google Books
(rating + count) to DB and metadata pipeline. PR #517 reserved `user_rating_overall`,
`user_rating_story`, `user_rating_performance`, `user_rating_notes` on `books` table.
PR #520 wires Audible `runtime_length_min` into candidate scoring. Still needed:

- [x] Audible ratings ingested (overall/story/performance/concept/delivery + count + reviews) — PR #516
- [x] Google Books ratings ingested (rating + count) — PR #516
- [x] User rating columns reserved on `books` table — PR #517
- [x] Duration scoring for candidates from Audible runtime — PR #520
- [x] **RATE-1** `PATCH /api/v1/audiobooks/:id/rating` accepts `{overall, story, performance, notes}` — PR #542
- [x] **RATE-2** Book detail UI: star rating widget (overall / story / performance + notes) — PR #552
- [x] **RATE-3** Audible/Google ratings shown on MetadataReviewDialog candidate cards — PR #553
- [x] **RATE-4** Library search/filter with numeric operators (>, <, >=, <=, ==, !=) for user_rating_* — PR #554
- [x] **RATE-5** Bulk rating view / quick-rate from list

---

## ⏱️ Audible Runtime vs Book Duration Mismatch Detection

Audible returns `runtime_length_min` for every product. We now store `Duration`
on the `books` table (set during metadata apply). These two numbers should be
within ~10 minutes of each other — large gaps (> 10 min) suggest the wrong
Audible product was matched or the file is an abridged version.

- [x] WARN log + `duration_mismatch` flag on candidate result when delta > 600s — PR #549
- [x] `GET /api/v1/maintenance/scan-duration-mismatch` bulk scan endpoint — PR #549
- [x] **DUR-1** Surface in `MetadataReviewDialog`: show a yellow warning chip on the candidate row when `audible_runtime_min` and book `duration` differ by > 10 min, e.g. "⚠ runtime differs by 45 min" — chip implemented at `MetadataReviewDialog.tsx:604`
- [x] Book detail panel: show Audible runtime alongside local duration so mismatches are obvious — PR #561
- [x] Threshold configurable via query param `?max_delta_min=10` — PR #549

---

## 🔒 Deluge Protected Paths — Reflink Import Workflow

**Spec:** [`docs/superpowers/specs/2026-04-29-deluge-protected-paths-design.md`](docs/superpowers/specs/2026-04-29-deluge-protected-paths-design.md)

Core rule: never edit files outside `RootDir`. Deluge files are reflinked into the library before any tag write, then `core.move_storage` keeps Deluge seeding from the new location.

Implementation steps (in order):

- [x] **DELUGE-1** `deluge_hash`, `deluge_original_path`, `imported_from_deluge_at` columns on `book_files` — PR #540
- [x] **DELUGE-2** `protectedPathCache` with TTL refresh + IsProtected() — PR #556
- [x] **DELUGE-3** `importToLibrary`: reflink `src → library_path`, update DB, call `core.move_storage` if enabled (best-effort). Implemented in fleet branch `fleet/014-deluge-3-import-to-library` (PR #976). Bot-task: [`docs/superpowers/bot-tasks/2026-04-29-deluge-3-import-to-library.md`](docs/superpowers/bot-tasks/2026-04-29-deluge-3-import-to-library.md)
- [x] **`WriteTagsSafe`**: pre-flight guard wrapping all tag-write call sites; falls back to `os.Copy` on non-reflink FS — ✅ verified done 2026-07-01: internal/fileops/write_tags_safe.go:35 (pre-flight hash, temp-copy+rename, non-reflink fallback)
- [x] **Migrate all call sites** [hold] to `WriteTagsSafe` (bulk write-back, single-file write, cover embed) — ✅ verified done 2026-07-01: all tag-write sites route through WriteTagsSafe (writeback/single-file/cover embed)
- [x] **Discovery → Import UI**: "Import" button on discovered torrent calls the import flow — PR #562
- [x] **UI**: "Imported from Deluge" badge on book detail; original path shown in Files tab audit row — PR #561
- [x] **Config**: add `protected_paths []string` field; expose in Settings UI — PR #562

---

## 🔗 iTunes Relink — Unresolved Cases

PR #507 shipped the iTunes relink endpoint (3-tier path resolver: same-dir M4B → flat iTunes
search → disambiguation). It resolved **94.7%** of broken organizer-root books. Three groups
of cases remain:

**13 manually-identified unresolved books** — documented in [`docs/reports/unresolved-relinks-2026-04-28.md`](docs/reports/unresolved-relinks-2026-04-28.md). Root causes: co-author directory mismatch (organizer uses plain author, iTunes uses `Author, Co-Author`), title prefix collision after colon→underscore substitution, and zero-match disambiguation edge cases.

**~6,719 missing-file-repair unresolved** — books whose organizer-root paths cannot be found
anywhere (not in iTunes, not as flat M4B). Many are likely Deluge-only files not yet imported.

- [x] **RELINK-1** Apply 13 manual path fixes from the report — bot-task spec: [`docs/superpowers/bot-tasks/2026-04-29-relink-manual-fixes.md`](docs/superpowers/bot-tasks/2026-04-29-relink-manual-fixes.md) — 9 fixed via API, 4 absent from iTunes (results: `docs/reports/relink-manual-fixes-result-2026-04-29.md`)
- [x] **RELINK-2** Co-author dir matching: tries all dirs where author's surname appears — implemented at `maintenance_fixups.go:4154`
- [x] **RELINK-3** Title prefix colon→underscore normalization — implemented at `maintenance_fixups.go:4257`
- [x] **RELINK-4** `GET /api/v1/maintenance/relink-report` re-runs dry-run with why_unresolved annotations — PR #555
- [x] **RELINK-5** Bulk-import Deluge files into library for the ~6,719 that are Deluge-only — depends on Deluge Protected Paths (see below) — PR #563

---

## 📡 Activity Feed — Follow-up Gaps

PRs #509, #518, #521 wired batch logging + EmitInfo summaries + no-op tag filtering for
the four scheduler-driven maintenance ops. A few gaps remain:

- [x] **ACT-1** series-normalize EmitInfo (dedup-scan/author-dedup-scan already covered) — PR #547
- [x] **ACT-2** `info` tier in default-on tier filter — PR #539
- [x] **ACT-3** Batch noun for `isbn-enrich` — implemented at `batcher.go:211`

---

## 🏷️ Audible Category Ladders → Book Tags

Audible's `category_ladders` response group returns a full hierarchy per book,
e.g. `Audible Books > Science Fiction > Space Opera`. Each layer should be
applied as a user tag on the book so browsing by genre is hierarchical, not flat.

- [x] **CAT-1** category_ladders parsed into CategoryTags + AddBookTagWithSource("audible_category") in apply pipeline — PR #548
- [x] Parse ladder entries into `BookMetadata.CategoryTags []string` (all layers, e.g. `["Science Fiction", "Space Opera"]`) — PR #548
- [x] In the apply pipeline, write each tag via `AddBookTagWithSource` (idempotent) with source `"audible_category"` — PR #548
- [x] UI: show Audible-sourced category tags separately from user tags in the book detail panel — PR #561
- [x] Search/filter: "has tag Science Fiction" or browsable tag cloud on library page  ✅ shipped #1728 (agent-task sweep 2026-07-01)

---

## 🤖 OpenAI Responses API Migration

Chat Completions is in maintenance; new models (gpt-5.4, codex-mini, the
o-series at full effort) ship on `/v1/responses` first or only. Plus
`PreviousResponseID` keeps history server-side, which collapses the
prompt-token cost for our multi-turn flows. Six phases sequenced
lowest-risk first; each phase ships independently and soaks before the
next picks up. Full plan in
[`docs/superpowers/specs/2026-04-29-responses-api-migration-design.md`](docs/superpowers/specs/2026-04-29-responses-api-migration-design.md).

- [ ] **AI-RESP-A** [hold] Migrate `metadata_llm_review.go` (single call) — design spec linked above
- [ ] **AI-RESP-B** [hold] Migrate `openai_parser.go` single-shot calls (6 sites) — depends on A clean
- [ ] **AI-RESP-C** [hold] **DO NOT MIGRATE EMBEDDINGS** — `/v1/embeddings` stays as-is. This entry is here only to make the bot aware not to touch `embedding_client.go`.
- [ ] **AI-RESP-D** [hold] Migrate Batches API (`openai_batch.go`) once OpenAI supports `/v1/responses` URLs in batch lines — verify endpoint allowlist before pickup
- [ ] **AI-RESP-E** [hold] Migrate `aijobs/aijobs.go` multi-turn flows — adds `last_response_id` to job state; biggest token win
- [ ] **AI-RESP-F** [hold] Cleanup: delete remaining Chat Completions call sites in `internal/ai/`

---

---

## 🩺 Diagnostics & Visibility

- [x] **DIAG-1** Fix `ApiError: store does not implement AIJobsStore` on Diagnostics page — `AIJobsStore` interface (`iface_misc.go:255-265`) has no methods implemented in `sqlite_store.go` or `pebble_store.go`; crash occurs when `batch_poller` asserts `s.Store().(database.AIJobsStore)` — PR #570
- [x] **DIAG-2** Expand Diagnostics to surface DB health — SQLite table row counts, PebbleDB key counts, embeddings DB stats, `ai_scans.db` stats, recently-rejected metadata with reasons, `metadata_fetch` cache hit/miss/age — PR #570
- [x] **DIAG-3** Surface `ai_scans.db` and `embeddings.db` stats in Diagnostics — both are opened in `server.go:934-1004` but never shown on the diagnostics or system-info pages — PR #570
- [x] **DIAG-4** Increase `MetadataFetchCacheTTLDays` default — metadata_fetch cache TTL (configured via `config.AppConfig.MetadataFetchCacheTTLDays`) is expiring too fast; increased default to 30 days — PR #570
- [x] **DIAG-5** Add path-prefix diagnostic to Storage page UI — `GET /api/v1/diagnostics/db-health` now returns `book_path_prefixes`; surface this in StorageTab so mismatches between configured import paths and actual stored paths are visible without a separate API call
- [x] **CACHE-FOLLOWUP-1** Metadata-fetch cache TTL enforcement — `GetCachedMetadataFetchWithMaxAge` centralizes the TTL check and emits `metrics.RecordCacheMiss("metadata_fetch","expired")`; `GetCachedMetadataFetch` is a backward-compat maxAge=0 wrapper; all 7 non-test callers updated; 3 new TTL unit tests — PR feat/metadata-fetch-ttl

---

## 🖥️ System Page Cleanup

- [x] **SYS-1** Remove duplicate log viewer from System page — System page uses `/system/logs` (a different endpoint and data model from Activity); replace with a navigation link to the Activity page
- [x] **SYS-2** Fix Storage page showing 0 books for `/mnt/bigdata/books/newbooks` — removed `is_primary_version` filter from `GetAllImportPaths` live subquery; added `GetBookPathPrefixes` diagnostic — PR #572

---

## 🔍 Data Quality & Matching Improvements

- [x] **MATCH-1** Deduplicate books by metadata URL/response hash — `metadata_source_hash` column added to `books` (migration 055); `sha256("{source}:{canonical_id}")` populated on metadata apply; duplicate count surfaced in BookDetail — PR #573
- [x] **MATCH-2** Consolidate multi-file chapter books by duration — files with sequential naming (`01 - Book`, `02 - Book`, etc.) that are individually very short (< 10 min each) should be pre-consolidated into a single book entry using cumulative duration rather than treated as separate books
- [x] **MATCH-3** Use duration as metadata scoring signal — boost metadata candidates whose Audible `runtime_length_min` roughly matches local file total duration; combine with existing title/author/series scoring for much higher confidence matches
- [x] **MATCH-4** Deduplicate on same-metadata-hash at import time — when a new book is scanned and its computed `metadata_source_hash` matches an existing book, flag as potential duplicate via dedup candidate (PR #1080). Computes hash at import time based on metadata source (audible/openlibrary/google_books/hardcover) + external ID; creates candidate with layer `metadata_hash_match` + similarity 1.0; logs "import: metadata hash duplicate detected"

---

## 🔐 File Identity & SHA Tracking

- [x] **FILE-SHA-1** Pre-metadata-write SHA capture — `original_file_hash` recorded before any tag write; `post_metadata_hash` column added to `book_files` (migration 053); `UpdateBookFileHashes()` wired around all write-back paths — PR #571
- [x] **FILE-SHA-2** Cross-folder duplicate detection via SHA — use `original_file_hash` to identify identical files across different library paths (e.g. same file in iTunes + Deluge + organized); surface as consolidation candidates in the dedup UI

---

## 🗃️ Rejected Metadata Store

- [x] **META-REJ-1** Rejected metadata tracking — `metadata_rejections` table (migration 054); `RejectedMetadataStore` interface; SQLiteStore + PebbleStore implementations; `GET /api/v1/audiobooks/:id/metadata-rejections` endpoint; rejection history collapsible section in BookDetail UI — PR #571

---

## 🖼️ UX Polish — Spacing & Layout

- [x] **UX-FOOTER** Footer spacer on every page — `MainLayout.tsx` now renders a 56px `aria-hidden` spacer after `{children}` so content never bumps the bottom edge of the viewport

---

## 🔄 Async Backfill Operations — Queue, Bell, Resume

All backfill handlers currently run **synchronously inside the HTTP request**. If the server
restarts mid-run they silently stop and will not auto-resume. They also don't appear in Active
Operations or the notification bell. These need the same treatment as `composer_tag_scan` and
`missing-file-repair`: `s.queue.Enqueue` → `operations.SaveParams` → `SaveCheckpoint` loop →
`activity.EmitInfo` summary on finish.

- [x] **BACKFILL-ASYNC-1** `handleBackfillFileHashes` — convert to async queued operation: — ✅ verified done 2026-07-01: internal/maintenance/jobs/backfill_file_hashes.go (async, checkpoint/resume)
  - `operations.BackfillFileHashesParams{DryRun bool}` struct in `state.go`
  - Enqueue as `"backfill-file-hashes"`, return `opID` immediately
  - Worker loop: for each `book_file` missing hash, `SaveCheckpoint` every N files
  - On restart: `LoadCheckpoint` → skip already-processed file IDs (by index or file_id cursor)
  - `activity.EmitInfo` summary on completion; `activity.LogBatch` for errors
  - Poll via `GET /api/v1/operations/{id}`; UI "Backfill Missing Hashes" button uses opID

- [x] **BACKFILL-ASYNC-2** `handleBackfillMetadataSourceHash` — same async treatment: — ✅ verified done 2026-07-01: internal/maintenance/jobs/backfill_metadata_source_hash.go (async)
  - `operations.BackfillMetadataHashParams{DryRun bool, Force bool}` struct
  - Enqueue as `"backfill-metadata-source-hash"`, return `opID`
  - Worker: iterate all books, checkpoint every N; skip-on-resume by `PhaseIndex`
  - `activity.EmitInfo` + `activity.LogBatch` on finish

- [x] **BACKFILL-ASYNC-3** [hold] `MetadataHashDuplicateCard` UI — add coverage stats panel + backfill button matching the SHA Duplicate Detection card style: — ✅ verified done 2026-07-01: MetadataHashDuplicateCard (MaintenanceTab.tsx:672) + book-metadata-hash-stats endpoint
  - `GET /maintenance/metadata-hash-stats` endpoint: total books, with/without `metadata_source_hash`, by-library breakdown
  - `BookMetadataHashStats` struct in `store.go`; `GetBookMetadataHashStats` in interface + SQLite + PebbleDB + mock
  - Auto-load stats on mount; status chip ("N missing hashes" / "✓ All hashed"); "Backfill Missing Hashes" button
  - Make sure `metadata_source_hash` is set in every metadata-cache path (already set in `ApplyMetadataCandidate`; verify fetch-cache replay path sets it too)

---

## 🔐 File Provenance / Hash Chain

Track the full lifecycle of a file's hash so we can answer "has this file changed since download?".
Proposed chain: **DownloadHash** (as-downloaded) → **OriginalFileHash** (after iTunes/external tagger) → **FileHash** (current, after AO).

- [x] **HASH-CHAIN-1** Add `download_hash` column to `book_files` (SQLite migration + PebbleDB field). Populate it from Deluge import data (already have `deluge_hash`) and allow manual set via API.  ✅ shipped #1722 (agent-task sweep 2026-07-01)
- [ ] **HASH-CHAIN-2** [hold] UI: show hash chain in book file detail view so users can see when/where a file changed.
- [x] **HASH-CHAIN-3** Integrity alert: flag files where `file_hash != original_file_hash` and no AO tag-write is on record (possible external modification / bit-rot).  ✅ shipped #1726 (agent-task sweep 2026-07-01)

*Low priority — AcoustID fingerprinting covers the identity-across-re-encode case better. Useful mainly for strict download-integrity auditing.*

---

## 🎵 AcoustID / Audio Fingerprinting — Stats & Trigger UI

AcoustID segment fingerprints already exist in the schema (`acoustid_seg0`–`seg6`). Needs the same coverage-stats + backfill-trigger treatment as file hashes.

- [x] **ACOUSTID-STATS-1** `GetAcoustIDStats()` — count books/files with ≥1 fingerprint segment populated, by-library breakdown. Add to interface + SQLite + PebbleDB + mock. — ✅ verified done 2026-07-01: GetAcoustIDStats() iface_misc.go:186 + Pebble + mock, by-library breakdown
- [x] **ACOUSTID-STATS-2** `GET /maintenance/acoustid-stats` handler + route. — ✅ verified done 2026-07-01: GET /maintenance/acoustid-stats handleGetAcoustIDStats (maintenance_fixups.go:553)
- [x] **ACOUSTID-STATS-3** UI card on Maintenance tab (same tile style as SHA Duplicate Detection): shows coverage %, "Fingerprint Library" trigger button, status chip. — ✅ verified done 2026-07-01: AcoustID coverage card MaintenanceTab.tsx:586
- [x] **ACOUSTID-DEDUP-1** Acoustic Duplicates tab in BookDedup — fingerprint-based candidate pairs with similarity scores (PR #998)
- [x] **ACOUSTID-COMPARE-1** Manual two-book fingerprint comparison — `GET /api/v1/acoustid/compare?a=&b=` with per-segment Hamming distance; comparison panel in UI (PR #999)
  - Both books/files displayed side-by-side (title, author, cover, duration, format)
  - Overall similarity score (0–100%)
  - Per-segment diff: seg0 (intro), seg1–5 (body), seg6 (outro) — each segment shown as a colored match/mismatch bar with its individual score
  - Clear visual indication of which segments match, which differ, and by how much

---

Statuses below reflect the current state including v0.206.0's shipped
work (many items marked "open" in the backlog file were quietly shipped
since it was last edited on 2026-04-11).

### 1. Dedup & Library Integrity — [section](docs/backlog-2026-04-10.md#1-dedup--library-integrity)

- [x] **1.1** `book_alternative_titles` schema + engine integration (#234)
- [x] **1.2** Duration-based similarity signal (shipped v0.206.0, commit `4c6139e`)
- [x] **1.3** Dedup scan as a real Operation (#227)
- [x] **1.4** LLM verdict auto-apply above confidence threshold (shipped v0.206.0, commit `28257a9`)
- [x] **1.5** Side-by-side metadata diff in cluster card (**M**) — MetadataDiffTable component #348
- [x] **1.6** Import-time collision preview (**M**) — #343
- [x] **1.7** Per-side "merge into this" quick action (#230)
- [x] **1.8** Smarter "split cluster" with edge preview (#233)
- [x] **1.9** Series-aware bulk merge (#232)
- [x] **1.10** Export dedup state as CSV/JSON (#231)
- [x] **1.11** **Async embed via OpenAI Batch API for nightly re-scans** — `dedup.embed-async` UOS op (nightly cron 03:00) + `POST /api/v1/dedup/embed-async` on-demand trigger; batch poller handles result ingestion (PR #1003)
- [x] **1.12** **Tag operation log lines with the originating operation ID** — pipe `op.ID` into a context-bound logger, replace bare `log.Printf` inside operation funcs with op-scoped calls, and write each line into `operation_logs` so the Activity-page log view shows everything (ffmpeg warnings, fingerprint failures, etc.) instead of only the explicit `progress.Log()` calls. Spec: [`docs/superpowers/bot-tasks/2026-05-04-tag-operation-logs.md`](docs/superpowers/bot-tasks/2026-05-04-tag-operation-logs.md) — ✅ verified done 2026-07-01: PR #1047 op-context logging: logging.Info(ctx) auto-prepends opID, writes operation_logs
- [x] **1.13** **Broken-files dashboard card + repair pipeline** — `book_file_errors` table, dashboard card, `has_file_errors` library facet, repair pipeline (PR #986)
- [x] **1.14** **Unified Operations System (UOS)** — COMPLETE 2026-05-11 (infra 2026-05-08, full migration 2026-05-11, final queue deletion PR #800). All 16 UOS tasks shipped across PRs #740–#759; v1→v2 `queue.Enqueue` migration completed across PRs #783–#798; BridgeQueue + OperationQueue + Queue interface fully deleted in PR #800. `scheduler_triggers.go` deleted; iTunes path ops and organizer scan decoupled from BridgeQueue via new `itunes_path_ops.go` and `ScanEnqueuer` callback. Single `Registry` owns every OperationDef; plugins register through `pkg/plugin/sdk`; subprocess isolation; explicit `ResumePolicy`; single SSE-fed frontend store. Human spec: [`docs/superpowers/specs/2026-05-04-unified-operations-system.md`](docs/superpowers/specs/2026-05-04-unified-operations-system.md).
  - [x] **UOS-01** Schema migrations for v2 tables (merged 2026-05-06)
  - [x] **UOS-02** Registry shell + dispatcher + in-process worker pool (PR #741, merged 2026-05-06)
  - [x] **UOS-03** Reporter DB writes + subprocess runner (PR #745, merged 2026-05-06)
  - [x] **UOS-04** Public plugin SDK at `pkg/plugin/sdk` + import lint tool (PR #746, merged 2026-05-06)
  - [x] **UOS-05** Frontend dual-source operations store (PR #740, merged 2026-05-06)
  - [x] **UOS-06** SSE EventHub + /operations/timeline + introspection endpoints (PR #748, merged 2026-05-06)
  - [x] **UOS-07** Canary — migrate `dedup.embed-scan` as the first live plugin op (PR #747, merged 2026-05-06)
  - [x] **UOS-08** Watchdog + op_strikes_v2 + startup resume orchestration (PR #744, merged 2026-05-06)
  - [x] **UOS-09** Migrate AcoustID + remaining dedup ops to UOS (PR #750, merged 2026-05-08)
  - [x] **UOS-10** Migrate iTunes plugin (5 ops) to UOS (PR #753, merged 2026-05-08)
  - [x] **UOS-11** Migrate Deluge plugin (3 ops) to UOS (PR #752, merged 2026-05-08)
  - [x] **UOS-12** Migrate 26 maintenance ops to UOS plugin (PR #751, merged 2026-05-08)
  - [x] **UOS-13** Frontend single-source — drop dual-source (PR #754, merged 2026-05-08)
  - [x] **UOS-14** Delete v1 OperationQueue + legacy endpoints (PR #756, merged 2026-05-08)
  - [x] **UOS-15** Promote pkg/plugin/sdk to stable public API + sdkguard CI (PR #755, merged 2026-05-08)
- [x] **1.15** [hold] **UOS amendment — `Reporter.SetCurrentItem(label)` for live "currently working on" ticker** — Sonarr/Radarr-style high-frequency current-item display under the progress bar. New SDK Reporter method that's purely ephemeral (in-memory on the registry's run handle, no DB write); SSE event `op.current_item` patches the frontend store; timeline endpoint returns the cached value so refresh / new tab / re-login re-hydrates. Survives refresh; survives a brief gap on server restart (next per-item iteration repopulates). If we ever want it to survive restart, retrofit is a single column add to `operations_v2` flushed at 30s cadence — explicit out of v1. Implementation footprint: amend §1 (Reporter) + §9 (timeline payload) + UOS-03/UOS-06 bot-tasks. Spec: [`docs/superpowers/bot-tasks/2026-05-05-uos-amendment-current-item.md`](docs/superpowers/bot-tasks/2026-05-05-uos-amendment-current-item.md). — ✅ verified done 2026-07-01: Reporter.SetCurrentItem reporter.go:32 + SSE current_item + useOperationsStore
- [~] **1.16** **Resizable + dynamically-sortable columns everywhere** — Library/Authors/Series/Works/TrashedVersions done (PR #1002). Remaining: dedup results, activity log, iTunes write-back preview, metadata review. Build a single `<ResizableSortableTable>` component (or extend existing `ConfigurableTable`); roll across remaining pages.
- [ ] **1.17** **Replace "AO" / "audiobook-organizer" branding with a real product name + logo** — the placeholder "AO" leaks into UI labels (e.g. proposed "AO Path" column on the iTunes write-back dialog), service names, status panels, etc. Pick a product name + minimal logo, apply consistently. Out of scope until name is decided; this entry is the placeholder for the rename sweep.

### 2. Known Bugs — all closed in #227

- [x] **2.1** Activity log compact "Everything (now)" returns 0
- [x] **2.2** Dedup scan isn't tracked in Operations (see 1.3)
- [x] **2.3** Dedup scan has no completion messages
- [x] **2.4** Directory organize has no cleanup on partial failure
- [x] **2.5** Scanner may double-count iTunes + organized paths as separate books
- [x] **2.6** `GetAllBooks` is O(n²) when called in a loop
- [x] **2.7** Auto-scan file watcher only watches one import path

### 3. Features — [section](docs/backlog-2026-04-10.md#3-features)

- [x] **3.1** Library centralization / `.versions/` layout (**L**) — 9/10 tasks (#296, #306, #315-#316, #324-#325, #337)
- [x] **3.2** Bulk organize undo via `operation_changes` (**M**) — 6/7 tasks (#318-#319, #326, #332)
- [x] **3.3** Bulk edit metadata across selected books (shipped v0.206.0)
- [x] **3.4** Smart playlists (**M**) — complete 9/9 (#307-#309, #338-#340)
- [x] **3.5** Cover art browse/restore UI (**S**) — #346
- [x] **3.6** Read/unread tracking (**M**) — complete 8/8 (#300, #303, #317, #331, #336)
- [x] **3.7** Multi-user support (**L**) — complete 8/8 (#292-#295, #313-#314, #322, #334)
- [ ] **3.8** Plex-style HTTP media server API (**L**)
- [ ] **3.9** [hold] LLM-based series detection and ordering (**M**)
- [ ] **3.10** [hold] AI-generated cover art when none exists (**S**)

### 4. Architecture / Future-Proofing — [section](docs/backlog-2026-04-10.md#4-architecture--future-proofing)

- [ ] **4.1** [hold] PostgreSQL research track (**XL**)
- [x] **4.2** Split the monolithic `server.go` (commit `c858ceb`)
- [x] **4.3** Move write-back queue to a durable outbox (**M**) — #344
- [x] **4.4** Replace `database.GlobalStore` package var with DI (**L**) — complete (#280-#291)
- [x] **4.5** Property-based tests for dedup engine (expanded to full codebase) (**M**) — complete (#357, #359, #361, #362, #363, #365, #366, #367, #368 — ~57 property tests across database / search / server / auth)
- [x] **4.6** Chaos tests for the embedding store under shutdown (**M**) — 7 tests: double-close, ops-after-close, concurrent write/read during close, mixed access, durability, WAL checkpoint
- [ ] **4.7** [hold] Per-workload store evaluation: Pebble vs SQLite vs PostgreSQL vs Go-native NoSQL (**L** research)
- [~] **4.8** Split the `database.Store` interface (ISP refactor) (**L**) — foundation + 3 proof-points shipped (#372, #376, #379, #380, #381, #382); ~38-file sweep + 18-file noop cleanup remain per [`docs/superpowers/plans/2026-04-17-store-iface-sweep.md`](docs/superpowers/plans/2026-04-17-store-iface-sweep.md)
- [x] **4.9** Eliminate remaining package globals (DI Phase 2) (**M**) — 10 globals replaced with interface injection + Server fields (#386)
- [ ] **4.10** [hold] Service-layer unit tests with mock stores (**L**) — leverage DI + ISP to unit-test AudiobookService, OrganizeService, MetadataFetchService, MergeService with MockStore; test error paths, edge cases, and business logic in isolation without HTTP or real DB  <!-- 2026-07-01: ◑ PARTIAL: unit tests exist for AudiobookService/OrganizeService/MetadataFetchService; MergeService lacks a dedicated mock-store unit-test file. -->
- [x] **4.11** Split `internal/server` into sub-packages (**XL**) — all 8 PKG tasks completed
  - ✅ **PKG-1** `internal/audiobooks/` — audiobook service extracted (#663)
  - ✅ **PKG-2** `internal/aiscan/` — AI scan pipeline extracted (#656)
  - ✅ **PKG-3** `internal/reconcile/` — reconcile logic extracted (#657)
  - ✅ **PKG-4a** `internal/scanner/` — scan service extracted (#658)
  - ✅ **PKG-4b** `internal/importer/` — import services extracted (#660)
  - ✅ **PKG-4c** `internal/quarantine/` — quarantine service extracted (#662)
  - ✅ **PKG-4d** `internal/writeback/` — writeback enqueuer/outbox extracted (#661)
  - ✅ **PKG-4e** `internal/fileops/` + `internal/sysinfo/` — filesystem/system services extracted (#664)
- [x] **4.12** Narrow extracted service dependencies to ISP sub-interfaces (**M**) — PR #995
- [x] **4.13** [hold] Extract iTunes integration into `internal/itunes` (**L**) — decouple iTunes import/sync/writeback from Server lifecycle; currently ~3,900 LOC deeply coupled to Server, needs interface extraction and dependency injection redesign — ✅ verified done 2026-07-01: internal/itunes/service extracted with Deps struct + narrow Store interfaces
  - [x] **4.13b** Unit tests for `internal/itunes/service/track_provisioner.go` — 11 tests: multi-segment, missing metadata, idempotency, UpsertBookFile error, managed/unmanaged paths, PID uniqueness, duration conversion, partial-failure best-effort. (`track_provisioner_test.go`, bot-task 4-13b)
  - [x] **4.13d** Error and edge-case tests for `internal/itunes/service/importer.go` (21 new tests; disabled-mode, corrupt ITL, concurrent sync, tombstoned PID, existing-PID link, SkipDuplicates, partial write, Sync GetAllBooks failure, cover-art missing, linkITunesMetadata, linkAsVersion, organizeOneBook nil/no-factory)

### 5. UX / DX Polish — [section](docs/backlog-2026-04-10.md#5-ux--dx-polish)

- [x] **5.1** Search inside the dedup tab (shipped v0.206.0, commit `191faa3`)
- [x] **5.2** "Similar books" lookup on BookDetail page (**S**) — #342
- [x] **5.3** Batch select in library view (**S**) — "Add to Playlist" batch action #345
- [x] **5.4** Better error messages on organize failures (#273)
- [x] **5.5** Dev mode "seed library" command (#274)
- [x] **5.6** Frontend test coverage baseline (**M**) — 22 test files / 160 tests: shared renderWithProviders + factories; component tests (SearchBar, ReadStatusChip, AddToPlaylistDialog, FilterSidebar); page tests (Playlists, Dashboard); CI: `make test-frontend`, `--run` flag, coverage thresholds
- [x] **5.7** API documentation (**M**) — OpenAPI 3.0.3 spec, 266 paths / 291 ops
- [x] **5.8** Regenerate ITL test fixtures after format work (**S**) — #348
- [x] **5.9** Enforce mockery-generated mocks via CI gate (commit `45492c3`)
- [x] **5.10** Fast-iteration backend test mode — `make test-short` + `testing.Short()` gates on 33 slow property tests (#384); `internal/server` drops from 760s → 63s

### 6. Integration / Ecosystem — [section](docs/backlog-2026-04-10.md#6-integration--ecosystem)

- [x] **6.1** Deluge `move_storage` integration (**M**) — #349
- [x] **6.2** Audnexus + Hardcover full integration (#7daef15)
- [x] **6.3** Tag writeback to iTunes via ITL updates (shipped previously)
- [x] **6.4** ITL upload / download / partial export (**M**) — all tasks done; partial export via `POST /api/v1/itunes/export-partial` (PR #1004)

### 7. Tagging as Infrastructure — [section](docs/backlog-2026-04-10.md#7-tagging-as-infrastructure)

Underlying tag plumbing shipped in #244. Most items below are follow-ons
that layer on that foundation.

- [x] **7.1** Tag-based policies / preference inheritance (**L**) — PR #997
- [x] **7.2** Language filter in metadata review (shipped v0.206.0, commit `df6c9bd`)
- [x] **7.3** Metadata-apply tagging — source + language (shipped v0.206.0, commit `441fd43`)
- [x] **7.4** Google Books → Audible auto-upgrade maintenance job (shipped v0.206.0, commit `24201d4`)
- [x] **7.5** Metadata fetch caching (shipped v0.206.0, commit `2080c87`)
- [x] **7.6** Persistent review dialog + concurrent review during fetch (shipped v0.206.0, commit `1d2bf53`)
- [x] **7.7** Author and series tag HTTP endpoints (**M**) — #347; frontend UI remains
- [x] **7.8** System tag UX — visual distinction user vs system (shipped v0.206.0, commit `4dda739`)
- [x] **7.9** Full iTunes library regenerate / rebuild (**L**) — diff-and-batch + full rebuild-from-scratch both shipped; `POST /api/v1/itunes/rebuild-full` (PR #1004)
- [x] **7.10** Archive sweep for soft-deleted books (**M**) — #342
- [x] **7.11** Author/series merge — sync denormalized `book.AuthorID` (shipped v0.206.0, commit `f244824`)
- [x] **7.12** Organize rewrites file tags on every run even when unchanged (shipped v0.206.0, commit `2d4ad01`)

### 8. Out of Scope / Decide Later — [section](docs/backlog-2026-04-10.md#8-out-of-scope--decide-later)

Intentionally deferred. Captured here so they don't resurface as new ideas.

- iOS / Android companion app (scope explosion)
- WebDAV browse of the library (niche)
- RSS / Atom feed of new additions (niche)
- Notification system (Slack / Discord when scan completes) (rabbit hole)
- Cross-library federation (architecturally premature)
- Voice control / Alexa skill (out of focus)
- Audio preview in dedup tab — play first 30 seconds (requires streaming infra)
- "Recommended for you" based on listening history (no listening history store)
- Book recommendation engine (same)

---

## 🧠 From Memory — items not yet in the backlog file

These surfaced in later sessions and live only in Claude project memory.
Promote to `docs/backlog-2026-04-10.md` (or a successor) when touched.

### Graceful File Ops — 1 remaining gap

Full details: [`memory/project_graceful_file_ops.md`](../../.claude/projects/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/memory/project_graceful_file_ops.md)

- [x] **GFO-1** UI indicator for in-flight file ops + `GET /api/v1/file-ops/pending` (#270)
- [x] **GFO-2** Per-book tracking key collision — moved to `pending_file_op:{bookID}:{opType}` (#270)
- [x] **GFO-3** Resumable ops — `bulk_write_back`, `isbn-enrichment`, `metadata-refresh` (#270), `reconcile_scan` (#272). ~13 cleanup/maintenance types still silently fail on restart but are low-impact.
- [x] **GFO-4** Phase checkpoints in apply pipeline — rename/tags/itunes phases skip on recovery
- [x] **GFO-5** `GET /operations/recent` ~900ms — fixed by replacing O(N²) bubble sort with `sort.Slice` (#270). Side-index deferred until benchmarks show it's needed.

### Series Name Normalization — shipped

- [x] **SNR-1** `StripSeriesContamination` pure function — strips dash-embedded title/position and trailing ordinal words from series names (`internal/metadata/series_normalize.go`)
- [x] **SNR-2** Ingest gates — `NormalizeMetaSeries`, `resolveSeriesID`, `ensureSeriesID` all call `StripSeriesContamination` before any store write
- [x] **SNR-3** `GET /api/v1/series/normalize/preview` — dry-run preview of rename/merge actions
- [x] **SNR-4** `POST /api/v1/series/normalize` — async remediation: rename → merge → write-back → organize
- [x] **SNR-5** `series_normalize` maintenance task registered in scheduler (manual-only)

### Bulk Metadata Review — Audible series format bug

Full details: [`memory/project_bulk_metadata_review.md`](../../.claude/projects/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/memory/project_bulk_metadata_review.md)

- [x] **BMR-1** Audible "Series, Book N" baked into series field — `normalizeMetaSeries` now runs in `ApplyMetadataCandidate` too, not just the auto-fetch paths (#271)

### Async Operations — Unified Maintenance System (✅ COMPLETE)

Unified Maintenance System shipped 2026-05-11 via `internal/server/maintenance_dispatcher.go`. All 28
`maintenance.Job` implementations in `internal/maintenance/jobs/` are accessible via
`POST /maintenance/jobs/:job_id` → enqueues as UOS op → returns `{ operation_id }`.

- [x] **ASYNC-0** Frontend: toast notifications for operation lifecycle — PR #499
- [x] **ASYNC-CORE-1** `MaintenanceJob` interface + registry — completed (`internal/maintenance/`)
- [x] **ASYNC-CORE-2** Dispatcher `POST /maintenance/jobs/:id` + resume — completed (`maintenance_dispatcher.go`)
- [x] **ASYNC-CORE-3** Frontend API client (`listMaintenanceJobs`, `runMaintenanceJob`) — completed
- [x] **ASYNC-CORE-4** Dynamic "Manual Fixes" section in MaintenanceTab — completed (`ManualFixesCard`)
- [x] **ASYNC-W1-1** Convert `fix-read-by-narrator` — ✅ `fix_read_by_narrator.go`
- [x] **ASYNC-W1-2** Convert `cleanup-series` — ✅ `cleanup_series.go`
- [x] **ASYNC-W1-3** Convert `fix-author-narrator-swap` — ✅ `fix_author_narrator_swap.go`
- [x] **ASYNC-W1-4** Convert `fix-version-groups` — ✅ `fix_version_groups.go`
- [x] **ASYNC-W2-1** Convert `backfill-book-files` — ✅ `backfill_book_files.go`
- [x] **ASYNC-W2-2** Convert `cleanup-empty-folders` — ✅ `cleanup_empty_folders.go`
- [x] **ASYNC-W2-3** Convert `cleanup-organize-mess` — ✅ `cleanup_organize_mess.go`
- [x] **ASYNC-W2-4** Convert `fix-library-states` — ✅ `fix_library_states.go`
- [x] **ASYNC-W3-1** Convert `enrich-book-files` — ✅ `enrich_book_files.go`
- [x] **ASYNC-W3-2** Convert `dedup-books` — ✅ `dedup_books.go`
- [x] **ASYNC-W3-3** Convert `fix-book-file-paths` — ✅ `fix_book_file_paths.go`
- [x] **ASYNC-W3-4** Convert `refetch-missing-authors` — ✅ `refetch_missing_authors.go`
- [x] **ASYNC-W3-5** Convert `recompute-itunes-paths` — ✅ `recompute_itunes_paths.go`
- [x] **ASYNC-CLEAN-1** Remove old synchronous maintenance routes — done (server.go 6400→581 lines)

### Design Spec Already Written (but not yet planned)

- [x] **DES-1** Bleve library search — complete 6/7 (#298, #301-#302, #311-#312, #321)
- [x] **DES-2** chromem-go embedding store — #351 (store impl + tests; dedup engine wiring follows)

---

## 📚 Implementation Plans — [`docs/superpowers/plans/`](docs/superpowers/plans/)

Every plan in chronological order. ✅ = implemented, ⏳ = design done, plan written, not yet executed.

- [x] [2026-03-10 Central logger](docs/superpowers/plans/2026-03-10-central-logger.md)
- [x] [2026-03-10 Incremental scan](docs/superpowers/plans/2026-03-10-incremental-scan.md)
- [x] [2026-03-12 Unified maintenance window](docs/superpowers/plans/2026-03-12-unified-maintenance-window.md)
- [x] [2026-03-14 Diagnostics export](docs/superpowers/plans/2026-03-14-diagnostics-export.md)
- [x] [2026-03-18 Files & History redesign](docs/superpowers/plans/2026-03-18-files-history-redesign.md)
- [x] [2026-03-25 Unified activity log](docs/superpowers/plans/2026-03-25-unified-activity-log.md)
- [x] [2026-03-25 Unified activity page](docs/superpowers/plans/2026-03-25-unified-activity-page.md)
- [x] [2026-03-27 ITL parser rewrite](docs/superpowers/plans/2026-03-27-itl-parser-rewrite.md)
- [x] [2026-03-28 Book-files table](docs/superpowers/plans/2026-03-28-book-files-table.md)
- [x] [2026-04-05 mTLS bridge](docs/superpowers/plans/2026-04-05-mtls-bridge.md)
- [x] [2026-04-06 Bulk metadata review](docs/superpowers/plans/2026-04-06-bulk-metadata-review.md)
- [x] [2026-04-06 mTLS bridge repo extraction](docs/superpowers/plans/2026-04-06-mtls-bridge-repo-extraction.md)
- [x] [2026-04-09 Activity log compaction](docs/superpowers/plans/2026-04-09-activity-log-compaction.md)
- [x] [2026-04-09 Embedding dedup](docs/superpowers/plans/2026-04-09-embedding-dedup.md)
- [x] [2026-04-10 Metadata candidate scoring PR1](docs/superpowers/plans/2026-04-10-metadata-candidate-scoring-pr1.md)
- [x] [2026-04-10 Metadata candidate scoring PR2](docs/superpowers/plans/2026-04-10-metadata-candidate-scoring-pr2.md)
- ⏳ [2026-04-15 Library centralization](docs/superpowers/plans/2026-04-15-library-centralization.md) — tasks 1-9 done (deluge integration deferred)
- [x] [2026-04-15 Bulk organize undo](docs/superpowers/plans/2026-04-15-bulk-organize-undo.md) — complete (tasks 1-6 + torrent move_storage PR)
- [x] [2026-04-15 Library centralization](docs/superpowers/plans/2026-04-15-library-centralization.md) — all tasks done including deluge integration (PR feat/deluge-centralization)
- ⏳ [2026-04-15 Bulk organize undo](docs/superpowers/plans/2026-04-15-bulk-organize-undo.md) — tasks 1-6 done (torrent move_storage deferred)
- [x] [2026-04-15 Smart + static playlists](docs/superpowers/plans/2026-04-15-smart-and-static-playlists.md) — complete (9/9 tasks)
- [x] [2026-04-15 Read/unread tracking](docs/superpowers/plans/2026-04-15-read-unread-tracking.md) — complete (8/8 tasks)
- [x] [2026-04-15 Multi-user support](docs/superpowers/plans/2026-04-15-multi-user-support.md) — complete (8/8, OAuth deferred)
- ⏳ [2026-04-15 Bleve library search (DES-1)](docs/superpowers/plans/2026-04-15-bleve-library-search.md) — tasks 1-6 done (skeleton through frontend)
- [x] [2026-04-15 DI migration (4.4)](docs/superpowers/plans/2026-04-15-di-migration.md) — complete

---

## 📐 Design Specs — [`docs/superpowers/specs/`](docs/superpowers/specs/)

- [2026-03-10 Central logger](docs/superpowers/specs/2026-03-10-central-logger-design.md)
- [2026-03-10 Incremental scan](docs/superpowers/specs/2026-03-10-incremental-scan-design.md)
- [2026-03-12 Unified maintenance window](docs/superpowers/specs/2026-03-12-unified-maintenance-window-design.md)
- [2026-03-14 Deferred iTunes updates](docs/superpowers/specs/2026-03-14-deferred-itunes-updates-design.md)
- [2026-03-14 Diagnostics export](docs/superpowers/specs/2026-03-14-diagnostics-export-design.md)
- [2026-03-15 External ID mapping](docs/superpowers/specs/2026-03-15-external-id-mapping-design.md)
- [2026-03-18 Files & History redesign](docs/superpowers/specs/2026-03-18-files-history-redesign.md)
- [2026-03-25 Unified activity log](docs/superpowers/specs/2026-03-25-unified-activity-log-design.md)
- [2026-03-25 Unified activity page](docs/superpowers/specs/2026-03-25-unified-activity-page-design.md)
- [2026-03-25 Unified change tracking](docs/superpowers/specs/2026-03-25-unified-change-tracking-design.md)
- [2026-03-27 ITL parser rewrite](docs/superpowers/specs/2026-03-27-itl-parser-rewrite-design.md)
- [2026-03-28 Book-files table](docs/superpowers/specs/2026-03-28-book-files-table-design.md)
- [2026-04-05 mTLS bridge](docs/superpowers/specs/2026-04-05-mtls-bridge-design.md)
- [2026-04-06 Bulk metadata review](docs/superpowers/specs/2026-04-06-bulk-metadata-review-design.md)
- [2026-04-06 mTLS bridge repo extraction](docs/superpowers/specs/2026-04-06-mtls-bridge-repo-extraction-design.md)
- [2026-04-09 Activity log compaction](docs/superpowers/specs/2026-04-09-activity-log-compaction-design.md)
- [2026-04-09 Embedding dedup](docs/superpowers/specs/2026-04-09-embedding-dedup-design.md)
- [2026-04-10 Metadata candidate scoring](docs/superpowers/specs/2026-04-10-metadata-candidate-scoring-design.md)
- [2026-04-11 Bleve library search](docs/superpowers/specs/2026-04-11-bleve-library-search.md) — design only, no plan yet
- [2026-04-11 chromem-go embedding store](docs/superpowers/specs/2026-04-11-chromem-go-embedding-store.md) — design only, no plan yet
- [2026-04-28 Unified maintenance system](docs/superpowers/specs/2026-04-28-unified-maintenance-system.md) — MaintenanceJob interface + registry + dispatcher (ASYNC-CORE + W1-W3 + CLEAN-1; awaiting Opus review)
- [2026-04-28 PR label dependency system](docs/superpowers/specs/2026-04-28-pr-label-dependencies.md) — GitHub label-based prerequisite tracking for multi-wave burndown bot work
- [2026-04-29 iTunes relink manual fixes](docs/superpowers/bot-tasks/2026-04-29-relink-manual-fixes.md) — bot-task spec for applying 13 known manual path corrections (RELINK-1)

---

## ✅ Recently Completed

### Session 23 (2026-04-29) — metadata pipeline + activity feed + ratings (#507–#521)

**15 PRs merged** across one session:

- **#507** `feat(relink)`: iTunes relink endpoint — 3-tier path resolver (same-dir M4B → flat iTunes search → disambiguation), dir-grouping, 94.7% success on ~8K broken paths. 13 unresolved cases documented in `docs/reports/unresolved-relinks-2026-04-28.md`.
- **#508** `feat(metadata)`: async resumable bulk-fetch-metadata for full library
- **#509** `fix(activity)`: wire `LogBatch` into purge-deleted, isbn-enrichment, temp-file-cleanup, missing-file-repair; rename `missing_file_repair` → `missing-file-repair` (dash consistency)
- **#510** `fix(mocks)`: add missing `GetAllBookFiles` typed expecter to `MockStore` (unblocked `TestMockStore_Coverage`)
- **#511** `fix(maintenance)`: `revert-metadata-fetch` endpoint
- **#512** `fix(metadata)`: bulk-fetch-metadata no longer auto-applies
- **#513** `feat(metadata)`: `prefer_audible` and `skip_cached` options for bulk-fetch
- **#514** `fix(audible)`: json/v2 compat — `DiscardUnknownMembers` + nullable `RuntimeLengthMin`
- **#515** `feat(audible)`: map `runtime_length_min` → `DurationSec` → `Book.Duration`
- **#516** `feat(ratings)`: full Audible (5 dims + count + reviews) + Google Books (rating + count) rating dimensions ingested and stored
- **#517** `feat(db)`: reserve user rating columns (`user_rating_overall/story/performance/notes`) on `books` table
- **#518** `fix(activity)`: emit EmitInfo summary entries so maintenance ops show content in activity log (not just start/complete)
- **#519** `fix(ui)`: MetadataReviewDialog refresh, regex filter, correct pagination + Deluge timeout fix
- **#520** `feat(scoring)`: duration-based candidate ranking from Audible runtime
- **#521** `feat(activity)`: no-op tag filtering — `EmitInfo` variadic tags, `NoOpTag`/`TagsIf` helpers, `ExcludeTags` SQL + HTTP param, frontend "hide no-op" chip (default on)

Missing-file-repair scan results: **9,034 fixed**, 36 ambiguous, **6,719 unresolved** (see RELINK-5).
CI: disabled Docker in prerelease workflow (was exhausting 14GB GitHub runner disk).

---

### Sessions 21-22 (2026-04-16) — feature foundations + v0.209.0/v0.210.0

**60 PRs merged (#280-#340)** across two sessions + 3 releases (v0.209.0, v0.210.0, v0.211.0):

- **4.4 DI migration** — complete (#280-#291): replaced `database.GlobalStore` with constructor injection
- **3.7 Multi-user auth** — tasks 1-4, 6 (#292-#295, #299, #313-#314): schema, permissions, middleware, lockout, 247-route permission wiring
- **3.1 Library centralization** — tasks 1-4 (#296-#297, #306, #315-#316): BookVersion schema, `.versions/` fs ops, primary swap, fingerprint check
- **3.6 Read/unread tracking** — tasks 1-4 (#300, #303, #317): position/state schema, recomputation engine, HTTP endpoints, iTunes Bookmark sync
- **DES-1 Bleve search** — tasks 1-5 (#298, #301-#302, #311-#312): index, parser, translator, indexedStore decorator, endpoint routing
- **3.4 Playlists** — tasks 1-3 (#307-#309): UserPlaylist schema, smart evaluator, 9 HTTP endpoints
- **3.2 Undo** — tasks 3, 5 (#318-#319): undo engine, pre-flight conflict detection
- **Bug fixes**: Pebble prefix iteration slice aliasing (#318), go.mod tidy for release (#310)
- **Releases**: v0.209.0, v0.210.0 published

### Session 20 (2026-04-14) — operations infrastructure + UX cleanup

- **#270** Per-op file I/O tracking + resumable bulk ops (GFO-1, GFO-2, GFO-3 partial, GFO-5)
- **#271** Normalize "Series, Book N" out of Audible candidates (BMR-1)
- **#272** Make `reconcile_scan` resumable (GFO-3 final)
- **#273** Richer organize error messages with paths and remediation hints (5.4)
- **#274** `seed` subcommand for local dev libraries (5.5)

### v0.206.0 release (2026-04-13)

See [v0.206.0 release notes](https://github.com/falkcorp/audiobook-organizer/releases/tag/v0.206.0) for the full commit list. Highlights folded into §1, §3, §5, §7 above.

<details>
<summary>Session 12-19 archive — click to expand</summary>

### Bugs — Session 15 (March 25-27, 2026) — all fixed
- **B1** Author merge variant display — shows merge target + all variant names
- **B2** Tag extraction conflicting metadata — composer cleared on write
- **B3** Author/narrator swap — mitigated by B2; full fix needs metadata pipeline redesign (7.11 covered the worst of it)
- **B4** `series_index` not read back — already fixed (reads `SERIES_INDEX` / `MVIN`)
- **B5** 35 iTunes sync path errors — not a bug, files genuinely missing on disk
- **B6** File version separator too faint — thicker separator
- **B7** Book detail refresh after metadata — refresh button + auto-refresh after operations
- **B8** Write-back fails on multi-file books — globs audio files in directory

### P0 / P1 — all resolved
- **1** ISBN enrichment wrong matches — 60% length ratio fix validated
- **2** Preview Organize (single book) — built with step-by-step preview + Apply
- **3** Playlist system — assessed, needs brainstorming (tracked as 3.4 above)
- **4** Bulk "Save to Files" — `POST /api/v1/audiobooks/bulk-write-back`
- **5** Series dedup cleanup — `POST /api/v1/maintenance/cleanup-series`
- **6** "read by narrator" fix — `POST /api/v1/maintenance/fix-read-by-narrator` (dry-run default)
- **7** M4B conversion live test — local tests pass; production test user-gated

### P2 items 8-29 (April 6, 2026 session) — all fixed
Activity page mobile layout, adaptive refresh, version vs snapshot UI polish, compare snapshot wiring, background ISBN enrichment, copy-on-write TTL tuning, iTunes PID detail view, ITL write-back testing, TAG-DIAG cleanup, author/narrator swap full fix, library state badges, Vite chunk splitting, stale interrupted operations, sticky settings buttons, iTunes sync dialog pre-fill, iTunes sync from ITL directly, Force Import greyed out, ITL multi-file books, Files & History separate version boxes, show individual files, track PIDs sorted, XML function deprecation.

### Active P1 items 30-33 (April 6, 2026) — resolved or partial
- **30** Background file ops graceful tracking — persistent PebbleDB tracking + startup recovery. Five follow-up gaps captured under **GFO-1..5** above.
- **31** Resume interrupted metadata fetch on startup — saves book_ids as params, resumes remaining
- **32** Aggressive search/book result caching — list 30s, metadata search 30s
- **33** Batch apply separate requests per click — partially fixed (500ms debounce); true client-side queue still open

### CI/CD & Lint Fixes (April 6, 2026)
- **34** E2E test lint errors — 15 fixes across 12 files
- **35** Frontend lint warnings — proper types, targeted eslint-disable
- **36** GitHub Actions Node.js 20 deprecation — `setup-node` already at v6.3.0; transitive updates ongoing

### Data Cleanup (Session 12-13)
- Library: 68K → 10.9K books (84% reduction)
- Authors: 6K → 2.9K; series: 19K → 8.5K
- 15K same-path duplicates, 5K same-format duplicates, 2.9K unmatched organizer copies deleted
- 1.3K duplicate series merged, 7.3K empty series removed
- 2.3K empty authors removed
- 278 numeric title prefixes stripped
- 332 fake numeric series assignments removed
- All ULID version groups converted to `vg-` style
- All version groups have a primary version set

### Features — Session 12-13
- Diagnostics page (ZIP export, AI batch analysis, 4 categories, results review)
- External ID mapping (migration 34, 97K PID mappings, merge/delete/tombstone)
- Files & History tab (format-grouped trays, TagComparison, ChangeLog timeline)
- Background ISBN/ASIN enrichment after metadata apply
- Bulk batch-operations API (per-item update/delete/restore)
- Universal batch poller (routes by metadata tag)
- Deferred iTunes updates (migration 33, post-transcode hook)
- File path history (migration 35)
- Genre field (migration 36)
- Copy-on-write backups with TTL cleanup
- Revert buttons in ChangeLog (DB + file revert)

</details>


## ⚠️ Automated Findings

- [ ] **DEDUP-CANDIDATE-EXPLOSION-2026-06-18** The `exact` dedup layer has **387,597 pending**  <!-- 2026-07-01: ⏳ = CONS-10 — prod backfill/investigation of the exact-candidate backlog; not a code deliverable. -->
  candidates (of 388,998 total; acoustid 1,028 / embedding 164 / llm 209 are sane) against only
  **49,573 final books** (`GET /api/v1/audiobooks` count) — yet memdb holds **401,968 raw `books`
  rows**. The exact emitters (`checkExactTitle` / `checkExactMetadataSourceHash` /
  `checkDurationMatch`) are pairing far beyond the final-book set. The engine *intends* primary-only
  (`internal/plugins/dedup/full_scan.go` "for every primary book"; `internal/dedup/engine.go`
  "skip non-primary versions"; `is_primary_version` filter), so either non-final/version-group/
  **chapter-as-a-book** rows leak past the filter (evidence: a candidate book titled *"Opening
  Credits"*), or this is a stale legacy backlog predating the primary-filter + `hasPlausibleAudio`
  fixes. **Investigate:** (1) how the ~352K extra book rows arise (chapter-split on import/organize?
  duplicate book rows per copy? soft-deleted/iTunes variants?); (2) whether candidate generation
  actually applies the primary filter on all exact emitters; (3) purge + rebuild candidates against
  final books. **Do NOT run `dedup.mine-gold-labels --apply` or bulk merge/dismiss until fixed** —
  it would seed the tuning dataset with within-group + chapter-artifact pairs. See
  [`docs/dedup-feedback-loop.md`](dedup-feedback-loop.md) §Open issue.

- [x] **FLAKY-DB-TESTS-2026-06-17** Two `internal/database` tests flaked under the full `Minimal CI / Go Tests (short, race)` run but passed in isolation. **Both root-caused + fixed (not quarantined)** — the "order-dependent state pollution" hypothesis was wrong for both; actual causes below. Verified: targeted `-race -count=20` green; both flakes reproduced before the fix (deterministic repro for #1; 2/20 failures for #2) and gone after.
  - `TestGetAcoustIDStats_Mixed` — **NOT** store/cache isolation. `GetAcoustIDStats` read book *files* pebble-direct but grouped them by library via `GetAllBooks`, which reads the **async-warmed memdb**. memdb write-through is a no-op until the warmup goroutine publishes (`memSync` returns early while `mem()==nil`), so under load the warmup published an empty/stale memdb → `GetAllBooks` returned no books → files collapsed into the `(unknown)` bucket → `LibraryRoot` assertion failed. Fix: added `getAllBooksPebbleScan()` and switched `GetAcoustIDStats` to read books pebble-direct (mirrors its existing file scan), so grouping no longer depends on warmup timing. Regression: `TestGetAcoustIDStats_StaleMemDBDoesNotBreakLibraryGrouping` injects an empty memdb post-write — fails pre-fix, deterministic post-fix.
  - `TestHNSW_RecallVsChromem` — **NOT** dataset seeding (data already fixed-seed). `hnsw.NewGraph()` seeds its level RNG from `time.Now().UnixNano()` → graph topology + recall varied ~0.75–0.92, dipping below the 0.80 gate. Fix: unexported `HNSWEmbeddingStore.newGraphRng` seam (nil in prod), test pins seed 1 → tight band (floor 0.868 / 50 runs) clearing 0.80 with margin. Not bit-deterministic (coder/hnsw v0.6.1 iterates Go maps in neighbor pruning) but dominant variance source removed; 0.80 gate kept so a real recall regression is still caught.
  - **Latent follow-up (not blocking):** the async-warmup write-through drop (`memSync` no-op while `mem()==nil`) is a general hazard for any "create then immediately read via memdb" test. Only `GetAcoustIDStats` was hardened. If other tests surface the same flake, add a `WaitForWarmup()` helper to `setupPebbleTestDB`.

- [x] **MEMLEAK-2026-06-14** [memory-leak] 4 potential memory leak(s) detected by scheduled scan — https://github.com/falkcorp/audiobook-organizer/actions/runs/27492872026. **Fixed by commit `4f68ef9f`** — all 4 timers tracked in `refreshTimeoutsRef`/`scrollTimeoutsRef` with unmount cleanup; `scripts/check-memory-leaks.py` reports clean. Issue #1449 closed 2026-06-17.
  - `src/components/dedup/UnifiedDedupTab.tsx:511` — Untracked setTimeout (may fire after unmount)
  - `src/components/dedup/UnifiedDedupTab.tsx:569` — Untracked setTimeout (may fire after unmount)
  - `src/pages/ActivityLog.tsx:250` — Untracked setTimeout (may fire after unmount)
  - `src/pages/ActivityLog.tsx:285` — Untracked setTimeout (may fire after unmount)

---

## 2026-05-01 Re-Audit Bot Tasks

Findings from the 2026-05-01 re-audit. See `docs/codebase-evaluation.md` §Re-Audit for evidence.

### High Priority

- [x] **TEST-1** Done: actual failures were missing `context.Context` args from CTX-3 (not PROJ-1/2); fixed `internal/fileops/service_test.go` and `internal/server/service_layer_test.go` — both packages now pass.
  Spec: `docs/superpowers/bot-tasks/2026-05-01-test-1-fix-audiobook-service-tests.md`

- **TEST-2** Fix `TestStoreAdditionalCoverageSQLite` failure in `internal/database` package  
  Spec: `docs/superpowers/bot-tasks/2026-05-01-test-2-fix-database-test-coverage.md`

### Medium Priority

- **DEP-1** Overview: migrate ~34 deprecated `Book.ITunesPath` usages across 4 packages to `BookFile.ITunesPath` (SA1019). See sub-tasks below.  
  Overview: `docs/superpowers/bot-tasks/2026-05-01-dep-1-migrate-itunes-path-field.md`

  - **DEP-1a** metafetch package — `batch.go` + `service.go` (~9 usages)  
    Spec: `docs/superpowers/bot-tasks/2026-05-01-dep-1a-metafetch-itunes-path.md`

  - **DEP-1b** organizer package — `service.go` (1 usage)  
    Spec: `docs/superpowers/bot-tasks/2026-05-01-dep-1b-organizer-itunes-path.md`

  - **DEP-1c** server handlers — `itl_rebuild.go` + `metadata_batch_candidates.go` (6 usages)  
    Spec: `docs/superpowers/bot-tasks/2026-05-01-dep-1c-server-itunes-path.md`

  - **DEP-1d** itunes/service package — `importer.go`, `path_reconcile.go`, `path_repair.go`, `writeback_batcher.go` (~14 usages)  
    Spec: `docs/superpowers/bot-tasks/2026-05-01-dep-1d-itunes-service-path.md`

  - **DEP-1e** (blocked on 1a–1d) DB migration to drop `books.itunes_path` column and remove sqlite_store.go usages

- **DEAD-1** Remove dead code: `legacySaveConfigToDatabase_REMOVED`, `bookTagKeyspace`, `bookSummarySelectColumnsQualified`, `linkAsVersion`, SA4006 unused values  
  Spec: `docs/superpowers/bot-tasks/2026-05-01-dead-1-remove-unused-code.md`

- **CTX-4** Thread `context.Context` through `ActivityStore.Summarize` and `CompactByDay` transactions  
  Spec: `docs/superpowers/bot-tasks/2026-05-01-ctx-4-activity-store.md`

- **PERF-1** Paginate 20+ unbounded `GetAllBooks(0,0)` calls in background jobs (OOM risk)  
  Spec: `docs/superpowers/bot-tasks/2026-05-01-perf-1-paginate-getallbooks.md`

### Low Priority

- **LOG-5** Replace remaining `fmt.Printf`/`log.Printf` in `sqlite_store`, `pebble_store`, `migrations`, `playlist`, `organizer` with structured `slog` calls  
  Spec: `docs/superpowers/bot-tasks/2026-05-01-log-5-remaining-printf.md`

- **R-9** Remove stale `// TODO: Implement in N1-2` comments from `sqlite_store.go:6913,6946` (already implemented)  
  Spec: `docs/superpowers/bot-tasks/2026-05-01-r9-remove-stale-todo-comments.md`

- **R-10** Fix 12 capitalized error strings in metadata packages (staticcheck ST1005):  
  `internal/metadata/audible.go`, `audnexus.go`, `googlebooks.go`, `hardcover.go`, `openlibrary.go`, `wikipedia.go`  
  Spec: `docs/superpowers/bot-tasks/2026-05-01-r10-fix-capitalized-error-strings.md`
