# Scope 08 — 20 items

## ITEM L4356 [tier B] section: `?version_group_id=` lists the whole library, and cannot be guarded
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Decide whether the list should filter on `version_group_id` at all. There
      is a real case: the memdb store already indexes it (`memIdxVersionGroupID`
      in `memdb_schema.go`) and `GetAllBooksFrom` accepts it as a filter key, so
      the storage layer supports the lookup the API does not expose.

## ITEM L4360 [tier C] section: `?version_group_id=` lists the whole library, and cannot be guarded
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] If yes: add a `case "version_group_id"` to `bookFieldValue` and the name
      to `allFilterFieldNames`. `TestFilterFieldNames_MatchTheMatcher` will hold
      the two together, and the bare-param guard then covers it automatically —
      no third list to update. Check the Pebble path too; a memdb-only index
      would be exactly the dual-implementation divergence fixed in #2406/#2410/#2411.

## ITEM L4365 [tier C] section: `?version_group_id=` lists the whole library, and cannot be guarded
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] If no: it still must not answer with the whole library. Extend the guard
      with a small set of *storage* filter keys that are not list filter fields,
      so the request is rejected rather than silently widened.

⚠️ Whichever way this goes, the rule from `FirstUnknownFilterField` applies: the
two failure modes here are inverted and both misleading — an unknown field
*inside* `filters` matches nothing and answers `count:0`, while a filter field
passed *bare* matches everything and answers with the library. Neither should be
reachable by a typo.

## ITEM L4375 [tier C] section: `?version_group_id=` lists the whole library, and cannot be guarded
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **124 files remain 0600 after the fix-file-modes repair (1,547 repaired),
      and they expose a stale-path defect.** The repair enumerates
      `GetAllBookFilesCore()`, but the residue files are on-disk paths the
      canary write-back REALLY wrote (mtime in the canary window) that do not
      appear in that enumeration. Worse: the sampled book's `/files` API row
      points at a path that does NOT exist on disk (`.../The Seven Deadly
      Demons 3 - Dungeon of Pride/Dungeon of Pride.m4b` → ENOENT) while the
      real file lives at `.../The Seven Deadly Demons/Dungeon of Pride/...`.
      So (a) some books' file rows carry stale paths, (b) the write-back
      resolves the REAL file anyway (different row? path fallback?), and
      (c) the repair job can't see those paths. Investigate the row-vs-disk
      divergence (organize moved files without updating rows? duplicate
      rows?), then either extend fix-file-modes with a disk-walk mode or
      repair the residue by hand:
      `sudo find <organizer-root> -type f -user <service-user> -perm 600 -exec chmod 664 {} +`

## ITEM L4417 [tier C] section: Search placeholder hint missing when navigating to All Books from Fini
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **ABS series list emits a non-ABS `books[]` shape, and no series render in ABS clients.**
      Measured 2026-08-16 against production with the client's exact query
      (`?page=0&limit=50&sort=name`, what AudioBooth actually sends).

      **Root cause (evidenced):** ABS defines a series' `books` as full
      `LibraryItem` objects. Ours emit six ad-hoc fields only:
      `duration, id, libraryId, libraryItemId, sequence, title` — no `media`,
      no `media.metadata`, no `mediaType`, no `coverPath`, no `path`/`ino`.

      The control that makes this conclusive is the **playlists** endpoint,
      which the same app renders correctly: its items embed a complete
      `libraryItem` with all 20 ABS fields including `media.metadata`,
      `coverPath` and `mediaType`. Same client, same auth, same library — the
      one with the correct shape works, the one with the ad-hoc shape does not.
      A typed (Swift) client decoding `books: [LibraryItem]` fails on the first
      entry and discards the whole response, which is why **23 of 50
      well-formed series still render as zero**.

      Ruled out — do not re-investigate:
      - Not a timeout: series is 20 KB in 0.34s; playlists is 131 KB in 3.2s
        and renders fine.
      - Not auth, not pagination, not the query params: HTTP 200,
        `results=50`, `total=15528`.

      **Secondary bug, worth fixing in the same pass:** 27 of 50 entries have
      `books: []`, and 9 of those are self-contradictory — `numBooks >= 1` with
      `books: []` and `totalDuration: 0` (e.g. "Salem's Lot (read by Ron
      McLarty)" reports `numBooks=1`). The other 18 report `numBooks=0`.

      Fix: build the series `books` array from the same library-item serializer
      the playlists path already uses, rather than a bespoke projection.

## ITEM L4449 [tier C] section: Search placeholder hint missing when navigating to All Books from Fini
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **The AI-scan cancel wiring is unverified, and it fails silently.**
      `CancelOperationV2` now cancels an AI scan through the pipeline manager
      (ported from the retired `DELETE /operations/:id`), but the collaborators
      arrive via `handlers.WithAIScanCancellation(...)` in `wire_handlers.go` and
      nothing asserts that call is still there. Drop it and cancelling an AI scan
      returns `204 No Content` while the scan keeps running — the exact defect the
      port exists to prevent.
      No test can cover it today because `Server.pipelineManager`
      (`*aiscan.PipelineManager`) and `Server.aiScanStore` (`*database.AIScanStore`)
      are concrete types, so a test cannot substitute them and drive the real
      construction path. Narrow them to the `ScanCanceler` / `AIScanLister`
      interfaces the handler already declares, then assert the wiring.
      Good candidate for the interface-splitting review.

## ITEM L4463 [tier C] section: Search placeholder hint missing when navigating to All Books from Fini
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **August executive-summary roundup is stale.** `2026-08-31-august-monthly-roundup-executive-summary.md`
      says it consolidates "the seven dated summaries ... from 2026-08-04 to 2026-08-09"
      and was last edited 2026-08-14, but the directory now holds individual summaries
      through 2026-08-16. It describes itself as "month in progress — updated as work
      lands", so it needs a consolidation pass covering 08-10 through 08-16 before the
      month closes.

## ITEM L4477 [tier C] section: Search placeholder hint missing when navigating to All Books from Fini
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Cancelling an operation the registry has never heard of reports success.**
      `DELETE /operations/v2/<unknown-id>` returns `204 No Content`. The handler
      calls `registry.Cancel(id)`, which returns `nil` for an id with no entry,
      so the route cannot distinguish "asked a running op to stop" from "did
      nothing at all". Measured 2026-08-16 in
      `TestOperationEndpointsErrors` — the assertion was written expecting 500
      and the test disagreed.
      This is the same shape as the legacy route it replaced, which answered 204
      after force-updating a legacy `operations` row that nothing was reading.
      Retiring that route did not fix the lie, it just stopped the write.
      Cancel should 404 for an unknown id and 204 only when something was
      actually signalled. Check whether the UI treats 204 as "cancelled" and
      shows a confirmation for an op that is still running.

## ITEM L4491 [tier C] section: Search placeholder hint missing when navigating to All Books from Fini
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **"Dynamic" collections are currently *manually* refreshed.** A query-backed
      collection is evaluated at creation, when its query is edited, when it is read
      through the native API, and when `POST /api/v1/collections/:id/materialize` is
      called. Nothing refreshes it in the background. The ABS read path deliberately
      never evaluates (it serves `MaterializedBookIDs`), so a collection created via
      the native API and then only ever viewed in the app shows its **creation-time**
      membership indefinitely. Smart playlists solved this with a `Dirty` flag plus a
      push worker; collections have no equivalent yet. Either add one, or rename the
      concept so the word stops promising more than it does.

## ITEM L4501 [tier C] section: Search placeholder hint missing when navigating to All Books from Fini
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`AddBookToCollection` is read-modify-write with no version check.** Two
      concurrent adds to the same collection can lose one, and now that any holder of
      `collections.manage` can edit server-wide rows, concurrent edits are a realistic
      shape rather than a theoretical one. `Collection.Version` already exists and is
      incremented by `UpdateCollection` — a compare-and-swap on it is the cheap fix.

## ITEM L4507 [tier C] section: Search placeholder hint missing when navigating to All Books from Fini
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`POST /api/session/local-all` 404s.** Observed from the app alongside the
      collections 404s on 2026-08-16. Separate ABS gap, not covered by #2498 — the
      `/api/session/` prefix is reserved, so this reaches the ABS surface and finds no
      route. Needs the same treatment: implement it, or confirm a 404 is the honest
      answer and record why.

## ITEM L4513 [tier C] section: Search placeholder hint missing when navigating to All Books from Fini
primary_domain_guess: internal/plugins/itunes | all_domains_guess: internal/plugins/itunes;internal/server/itunes_ops.go

- [ ] **Finish (or delete) the iTunes plugin op migration.** `internal/plugins/itunes/`
      holds five stub `Run` bodies. Four are excluded from `registeredDefs()` because
      the real implementations live in `internal/server/itunes_ops.go` and
      `itunes_path_ops.go`; the package is now half a migration that does nothing.
      Either port the real bodies in (`itunes.sync` additionally needs
      `s.activityWriter` + `s.itunesActivityFn` threaded into `Plugin`, which is a
      design decision, not a move) or delete the stub files and their defs. Leaving
      them is what caused #2490's sibling bug: a stub that looks registrable.

## ITEM L4521 [tier C] section: Search placeholder hint missing when navigating to All Books from Fini
primary_domain_guess: internal/itunes | all_domains_guess: internal/itunes

- [ ] **Wire `itunes.position-sync` or drop it.** `internal/itunes/service/position_sync.go`
      implements a full bidirectional bookmark/play-count sync (`PositionSync.Sync()
      (pulled, pushed int)`) and **nothing in the codebase calls it** — the only
      reference is the TODO comment in the plugin stub. Wiring it turns on real writes
      to user positions across two systems on a 63k-book library, so it needs an
      explicit decision and a dry-run, not a one-line hookup.

## ITEM L4548 [tier C] section: Search placeholder hint missing when navigating to All Books from Fini
primary_domain_guess: internal/aiscan | all_domains_guess: internal/aiscan

- [ ] **`ClearStaleOperations` is still wired, deliberately.** `POST
      /operations/clear-stale` force-marks pending/running/queued legacy rows as
      `failed`. It is the only broom for the ~183 historical rows stranded before
      the bridge landed, so deleting it now would remove the only tool for them.
      It is also dishonest for rows whose jobs actually completed — `failed` is
      not what happened. Retire it together with the supervised backfill in
      `todo.d/20260816-backfill-stuck-legacy-op-rows.md`, not before.
      Note `internal/aiscan/pipeline.go` still writes the legacy table directly at
      4 call sites, so "nothing writes it anymore" is not yet true.

## ITEM L4563 [tier C] section: Search placeholder hint missing when navigating to All Books from Fini
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`/tasks/*` and `/maintenance-window/*` are NOT v1 operations.** Six routes
      on the legacy operations handler are scheduler *configuration*, not
      operation records. They should not be converted to op-defs or deleted with
      the rest; move them to their own handler so "retire v1 operations" does not
      read as "delete task scheduling". Still outstanding.

## ITEM L4569 [tier C] section: Search placeholder hint missing when navigating to All Books from Fini
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`library.import`, `library.organize` and `library.transcode` still carry the
      4h ceiling and `ResumeDrop`.** Only `library.scan` was changed, deliberately —
      it is the one measured to exceed 4h. Check whether the others can also exceed
      their ceiling on a 63k-book library before assuming they are fine; `organize`
      in particular touches every book.

## ITEM L4575 [tier C] section: Search placeholder hint missing when navigating to All Books from Fini
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Convert the remaining long-running `ResumeDrop` ops to real resume.** The
      mechanism now exists: `registry.RunItems` gained `ResumeFrom`,
      `CheckpointEvery` and `CheckpointStateFn` (concurrent-safe via a
      contiguous-completion watermark), and 51 call sites route through it. As of
      2026-08-17 the live registry reports 140 defs: 100 `drop`, 19 `restart`, 19
      `requeue`, 2 `ask`. Work through the `drop` list and convert the ones that are
      both long-running and idempotent per item — `metadata.batch-apply-cached`,
      `reconcile.apply` and the full-library sweeps first. Ops that are short-lived
      or unsafe to re-enter should STAY `drop` and get a comment saying why; an
      honest drop is better than a resume that does not work.

## ITEM L4586 [tier C] section: Search placeholder hint missing when navigating to All Books from Fini
primary_domain_guess: internal/organizer | all_domains_guess: internal/organizer;internal/reconcile;internal/scanner

- [ ] **Forward `IsCanceled()` through `reporterLogger` and exercise the four guards it wakes up.**
      `LoggerFromReporter` now bridges `UpdateProgress` to the ops registry
      reporter, but `IsCanceled()` still delegates to the wrapped logger, which
      answers `false` unconditionally. That leaves four cancellation guards
      unreachable, as they have been since the 2026-05-11 BridgeQueue removal:
      `internal/scanner/service.go:190`, `internal/organizer/service.go:897` and
      `:1082`, `internal/reconcile/reconcile.go:597`.
      Cancellation itself is not broken — every one of these services also
      honours `ctx`, which is what the watchdog cancels — so this is a
      responsiveness and correctness-of-intent item, not an outage. It was held
      back from the progress fix deliberately: switching on four branches that
      have not run in three months, in the same change that unblocks production
      scanning, would make a bad first run impossible to bisect.
      Before flipping it: read each guard for what it does on the way out
      (partial state, half-written aggregates, skipped cleanup), and check
      whether `scanner/service.go:177`'s "both cancellation channels have to be
      checked here" comment still describes the intended behaviour once the
      logger channel is live.

## ITEM L4605 [tier C] section: Search placeholder hint missing when navigating to All Books from Fini
primary_domain_guess: internal/logger | all_domains_guess: internal/logger

- [ ] **Audit the other two silently-stubbed `StandardLogger` methods.**
      `RecordChange` and `ChangeCounters` (`internal/logger/standard.go:62-63`)
      are also empty/nil, so any operation running through
      `LoggerFromReporter` that records changes is discarding them the same way
      progress was being discarded. Determine whether the scanner/organizer
      change-tracking counters are consumed anywhere (activity feed, op summary)
      and, if so, whether they have been empty since 2026-05-11.

## ITEM L4660 [tier C] section: Import-path scan no longer surfaces per-file scan errors
primary_domain_guess: docs | all_domains_guess: docs

- [ ] **3 scheduled tasks are ENABLED but can never run.** Startup logs
      `Scheduled task is ENABLED but can NEVER run` for `library_organize`,
      `library_size_refresh` and `metadata_upgrade` — all `interval=0s`,
      `declaresMaintenanceWindow=false`, `inMaintenanceOrder=true`. Pre-existing (15
      occurrences before the 2026-08-16 boot). Each needs either a
      `scheduled.<task>.interval` or `declaresMaintenanceWindow=true`.
      ⚠️ `library_organize` is the trigger for the library-wide relocation from #2479 —
      enabling it starts moving files across the whole library, so decide deliberately.
      See `docs/handoffs/2026-08-16-overnight-silent-failure-fixes.md`.

