<!-- file: docs/proposals/2026-10-holistic/01-legacy-and-dead-code.md -->
<!-- version: 1.2.1 -->
<!-- guid: 3b9e6a52-1f7d-4c08-9e2a-5d4b7c1f8e63 -->
<!-- last-edited: 2026-10-08 -->

# 01: Legacy patterns, dead code and useless logic

Analyst: `legacy`. HEAD measured: `f7211eb39`. Read-only; nothing in the repo was edited.
Appendices: [`01-legacy-and-dead-code/`](01-legacy-and-dead-code/) (full dead-function list,
route list, frontend list, config list, method).

> **Coordinator note (08, 2026-10-08).** Ownership settled by the roadmap: (1) P1 absorbs 07 R1 (the activity default flip); the NutsDB test port stays in P2. (2) P7 is the only PR that deletes the `vite.config.ts` `test:` block (06 P11 no longer does). (3) New **P73** takes the dedup category C cluster (705 lines), which 03 never picked up. (4) New **P74** owns the rest of the v1 `operation:` retirement, which 01 §6 and 05 §6 each handed to the other. See `08-integrated-roadmap.md` §3.

## 1. Summary

- **About 15,900 production lines could be deleted, in four tiers. The numbers are measured, not estimated.**
  - Tier 1, safe now: **4,674** lines (Go 2,449, TS and config 2,225).
  - Tier 2, needs a per-item check: **3,162** Go lines.
  - Tier 3, needs an owner decision: **7,342** lines (2,161 unwired-feature Go lines, plus the 5,181-line SQLite activity backend).
  - See 03 (dedup engine): **705** lines.
  - About 6,360 test lines would go with them, and a few test files need porting (§4).
  - Method is in [`appendix A`](01-legacy-and-dead-code/A-method-and-totals.md).
- **The NutsDB activity stack is finished migration debt.** Five files and the `nutsdb` dependency have no production caller. A trial deletion in a scratch copy builds green (`go build ./...`) and removes **3,292 net production lines** (together with `internal/download`).
- **The SQLite activity backend is still the code default, though the owner retired it on 2026-09-19.** Only `deploy/local.conf` keeps prod on Pebble. If that file is lost, prod silently goes back to SQLite, and that rollback is not symmetric. Flipping the default is an S-sized change and should ship first.
- **Ops v1 is still read in 10 places, but not the same 10 the memory note lists.**
  - The startup resume sweep and two dashboard readers are gone.
  - New since the note: `pause.go:124`. It reports "running ops" from the v1 table, which nothing has written since 2026-08-23, so the list is always empty.
  - The v1 reaper still unmarshals and sorts the whole `operation:` keyspace every minute, and it can never match anything.
- **The `opstate:<id>:params` side table has 2 writers and 0 readers.** `LoadParams`, `LoadRawParams` and `SaveRawParams` are unreachable. The organizer writes an empty struct into it on every organize.
- **The ModifyBook migration is complete except for one path.** 10 direct `UpdateBook` sites remain and 9 of them are justified. The exception is `organizer/rename.go:139→226`, a read, then a file move, then a whole-row write. It is reachable only from `POST /audiobooks/:id/rename/apply`, and that route has no in-repo caller, so the route and its path are a deletion candidate, not a conversion. The second half of the migration, the row-version check in `UpdateBook`, is still not built.
- **`Book.FilePath` is still referenced on 483 non-test lines (416 reads, 67 writes)**, even though the field is stale by design. This is the largest unfinished migration. It is a design item for 07; this doc gives only the census.
- **Fourteen top-level `Config` fields are loaded but never read.** Several of them are Settings toggles: `create_backups`, `verify_after_write`, `embed_cover_art`, `language`, the four memory-limit fields, `log_format` and `enable_json_logging`. The UI promises control it doesn't have.
- **Frontend dead code:**
  - 12 source files are unused, or used only by their own tests (1,751 lines).
  - 35 `api.ts` client functions have no caller (458 lines).
  - The `test:` block in `vite.config.ts` is dead config, because `vitest.config.ts` wins and doesn't merge it.
  - 22 raw `fetch()` calls bypass `apiFetch`, so they miss the CF Access login-redirect detection and treat an expired session as success.
- **93 API routes have no caller anywhere in the repo.** 13 have known outside callers and 8 are dedup routes taken over from 03 (P72), which leaves **72** for owner triage. Each still needs an AudioBooth/curl check, so this is a floor, not a dead list. Owner triage is in [`appendix C`](01-legacy-and-dead-code/C-routes-without-in-repo-caller.md).

## 2. Findings

Confidence is per the charter. "Lines" means production lines unless marked otherwise. Func-lines are measured from the `go/ast` span of each function, including its doc comment. File-lines are measured with `wc -l`.

### 2.1 Unfinished migrations

| ID | Finding | Evidence | Conf. | Impact |
|---|---|---|---|---|
| M1 | **NutsDB→Pebble activity migration is done, but the NutsDB code is still here.** `NutsActivityStore`, `NutsMetricsStore`, `DualWriteActivityStore`, `InstrumentedActivityStorer`, `BackfillNutsActivityToPebble`. `activity/register.go:80-84` hard-errors without Pebble ("No more NutsDB fallback"). Only `actTiers`, `actCompactableTiers` and `matchesFilter` (~40 lines in `nuts_activity_store.go`) are live, and they are used by the Pebble store. | `deadcode` lists 63 funcs (1,668 func-lines). The trial deletion builds green; type-switch arms at `sql_activity_migrating_store.go:319` and `activity_partial_query.go:40` must go in the same PR. `go.mod:18` `nutsdb v1.1.0` has no remaining importer. | high | −1,950 prod lines, −1 dependency. Tests that use Nuts as a convenience backend need porting to Pebble: `activity/service_test.go`, `activity/writer*_test.go`, `server/activity_*_test.go`, `database/activity_compact_test.go`, `pebble_activity_store_test.go:581-667`, `pebble_activity_filter_index_test.go:425,600`. |
| M2 | **The SQLite activity backend is retired in prod but is still the code default.** `config.go:1077-1084` documents "sqlite (default, empty ⇒ sqlite)". `activity/register.go:91-130` builds `MigratingActivityStore` unless `ActivityBackend=="pebble"`. Prod is on Pebble only through `deploy/local.conf`, a gitignored file. | `wc -l`: 11 production files, 5,181 lines (`database/sql_activity_*.go`, `activity/sql_migration*.go`); 5,373 test lines. | high | The default-flip PR is S-sized and removes the risk that a lost conf file silently re-engages SQLite. Deleting the backend is an owner decision (Q2). |
| M3 | **Ops v1 is write-dead and read-alive.** Live v1 readers at HEAD, re-measured:<br>(a) the reaper, `server_lifecycle.go:449-464,1679-1727`;<br>(b) `handlers/operations/pause.go:124`, which is new;<br>(c) `CancelOperation`, `handlers/operations/handler.go:162-226`, which no route references anymore (see D3);<br>(d) `sysinfo/service.go:324` (`/system/logs`, whose only client `getSystemLogs` is unused);<br>(e) the history bridges `maintenance_fixups.go:552`, `metabatch/fetch_ops_index.go:165` and `maintenance/jobs/revert_metadata_fetch.go:79`, each read v2 first;<br>(f) the v1 retention purge, `maintenance/jobs/retention_and_hygiene.go:180,204,281`;<br>(g) `registry/legacy_op_status.go:218,231` and `registry/legacy_backfill.go:142,218`.<br>Gone since the 09-07 note: `handlers/system/handler.go:697`, `diagnostics/service.go:386`, `sysinfo/service.go:143`, and the whole `resumeInterruptedOperations` sweep. | `grep -rnE '\.(GetOperationByID\|GetRecentOperations\|ListOperations\|UpdateOperation(Status\|Error)\|DeleteOperationWithLogs)\(' internal` | high | Retiring v1 is a 05 project (§6). This doc proposes deleting only (a), (b) and (c), which are inert or buggy today. |
| M4 | **The `opstate:<id>:params` side table** has 2 writers, `organizer/service.go:286` (it writes an empty `OrganizeParams{}`) and `itunes/service/importer.go:359`, and **0 readers**. `operations/state.go` `LoadParams`, `LoadRawParams` and `SaveRawParams` are unreachable. | deadcode with `-test` still flags `state.go:176,193,202`; `BulkWriteBackParams` at `state.go:48` has no user. | high | Delete the pair, `GetOperationParams` and `SaveOperationParams` from `iface_ops.go:34`, `server_ops_store.go:227`, `state.go:129` and both mocks. Keep the `opstate:` *state* half (it is live). |
| M5 | **The ModifyBook migration is done except for one live path.** 10 direct `.UpdateBook(` call sites against 137 `.ModifyBook(` call sites. Justified whole-row writes: `organizer/service.go:1419,2490`, `handlers/organize.go:411`, `merge/combine_journal.go:1453`, `migrations.go:1229`, `pebble_store.go:3654`, `indexed_store.go:104`, `organizer/move.go:85` (test-only `MoveBookFile`), and `dedup/book_dedup.go:693` (dead `MergeBooks`, see 03). **Unjustified: `organizer/rename.go` `ApplyRename`**, which reads at :139, moves a file, then writes the whole row at :226. Its only entry point is `POST /audiobooks/:id/rename/apply` (`wire_library_routes.go:66`), and its only client `applyRename` (`api.ts:5791`) has no caller. The row-version guard in `UpdateBook` (the second half of the 09-13 owner decision) does not exist; `grep RowVersion\|ErrStaleWrite internal/database` returns nothing. | grep counts | high | Q4: delete the rename preview/apply routes, or convert them. Build the row-version guard so the pattern cannot come back (L). |
| M6 | **`Book.FilePath` is a stale field that is still read everywhere.** 483 non-test reference lines: 416 reads, 67 writes. Top packages: `plugins/maintenance` 63, `organizer` 53, `metafetch` 53, `database` 40, `scanner` 37, `audiobooks` 34. There is no shared "book's real files" helper (a `^func .*(Primary\|Active).*File` search finds none). | typed `go/packages` census ([appendix A](01-legacy-and-dead-code/A-method-and-totals.md)) | high | L. The design belongs to 07; recommend a helper on top of `GetBookFilesForIDsCore` first, then a read-site sweep by package. |
| M7 | **Compat shims (`docs/compat-surfaces.md` is stale).**<br>`server/file_move.go` (18 lines): no callers.<br>`server/pipeline_checkpoint.go` (41): test-only.<br>`server/deluge_importer_adapter.go` (25): fully dead.<br>`server/file_pipeline.go`: already gone.<br>`audiobooks/rename.go` and `organize_preview.go` are **not** pure shims: they bind live callbacks (`IsProtectedPath`, `FilterUnchangedTags`, `WriteTags`, ...). They are reached through a **second** re-export layer, `server/audiobooks_compat.go` (171 lines), from `wire_handlers.go:732,746`. | The trial deletion failed on `audiobooks_compat.go:126-167` until the audiobooks files were restored. | high | Delete the 3 server files (84 lines). Make `wire_handlers.go` call `audiobooks.New*` directly and drop the matching aliases in `audiobooks_compat.go` (S). Update `compat-surfaces.md`. |
| M8 | **The frontend API client migration is unfinished.** 22 raw `fetch()` call sites outside `web/src/services/` skip `apiFetch` (`utils/apiFetch.ts:150`), so they get no `ApiLoginRedirectError` detection and no timeout. Top files: `pages/Users.tsx` (6, and `:82`, `:90` ignore `resp.ok`), `settings/DelugeSettingsTab.tsx` (4), `settings/PluginsTab.tsx` (3), `ChangeLog.tsx:101,114` (revert and write-back, both writes), `AnnouncementBanner.tsx:40` (duplicates the unused `api.getAnnouncements`). | `grep -rnE '\bfetch\(' web/src` minus tests, services and apiFetch | high | M, mechanical. The write sites are the priority: an expired session currently reads as success. |
| M9 | **Worker-pool API used sequentially.** `registry.RunItems` with no `Concurrency` set: `plugins/acoustid/reset_all.go:111,182`, `acoustid/lsh_backfill.go:109`, `deluge/path_update.go:118`, `deluge/centralization.go:104`. | `RunItemsOptions{...}` at each site has no `Concurrency` | medium | The Deluge ones may be sequential on purpose (RPC rate). The acoustid ones are Pebble-local and could run on `NumCPU`. Hand to 05. |

### 2.2 Dead code

| ID | Finding | Evidence | Conf. | Impact |
|---|---|---|---|---|
| D1 | **Go: 499 production funcs (8,098 func-lines) are unreachable from every `main`.** This excludes `pkg/`, `internal/writeback/`, test-support files and `*ForTest*` hooks. 99 of them (1,184 lines) are unreachable even from tests. Breakdown: A, superseded, 1,950 lines; B, unwired features, 2,161; C, dedup (see 03), 705; D, the rest, 3,282, of which 104 are test seams and 16 (`BackfillPebbleActivityToSQL`) are already counted in the SQLite backend, so tier 2 is 3,162. | `deadcode` x/tools v0.51.0. The result is identical for darwin and linux/amd64 and with or without the `embed_frontend` and `bench` tags. Rule 4: registry ops register by function value, which RTA sees; no `MethodByName` or `go:linkname` in the repo. Caveat: RTA marks exported methods reachable once a type escapes to `any`, so D3 shows the true count is higher. | high (A), medium (D) | See tiers in §3. Full list: [appendix B](01-legacy-and-dead-code/B-go-unreachable-functions.md). |
| D2 | **`internal/download`** (the qBittorrent, SABnzbd and Deluge download clients plus a factory) has **zero production importers**. 953 prod file-lines, 797 test lines. The live Deluge integration is a different package, `internal/deluge`. | `grep -rln 'internal/download"'` finds only its own tests | high | This is "built, never wired". Q3. |
| D3 | **Unrouted handler that deadcode misses:** `handlers/operations/handler.go:162 CancelOperation`, 72 lines. Its route was retired (`wire_operations_routes.go:76-86`), but the method still contains the v1 force-cancel fallback. Also unreachable: `Handler.resolveScheduler` (`:141`). | No `.CancelOperation` reference in non-test code | high | Delete. |
| D4 | **`server/validators.go`**, 300 lines, 14 funcs, all unreachable. These are not Gin validators; nothing registers them. | deadcode; trial deletion builds | high | Delete, plus its test. |
| D5 | **Unwired features, not superseded code. Don't delete without the owner.**<br>`fingerprint/window_similarity.go` and `book_signature.go` (the windowed matcher; see the "signals never scored" note).<br>`transcribe/classify.go` `TitleAgreement` and `IsLikelyMisfiled`.<br>`itunes/xml_export.go` and `plist_parser.go` `writePlist` (iTunes hands-off rule).<br>`telemetry.MetricsHandler` (Prometheus gap note).<br>`versions/swap.go` `RunVersionSwap` and `ResumeVersionSwaps`, with `deluge` `NotifyDelugeAfterVersionSwap`.<br>`tagger.EmbedCoverArt`, which pairs with the inert `embed_cover_art` config (C1). | deadcode category B: 111 funcs, 2,161 func-lines | high that they're unreachable; the decision belongs to the owner | Q3 and Q5. |
| D6 | **Frontend unused files** (knip 5, tests counted as entries): `FingerprintVisualsColumn.tsx` (156, handed over by 03), `LoadingSpinner.tsx` (36), `audiobooks/FileSelector.tsx` (138), `InlineEditField.tsx` (103), `MetadataDiffTable.tsx` (113), `TagEditor.tsx` (240), `filemanager/DirectoryTree.tsx` (167), `hooks/useTimeout.ts` (76), and `pages/FileManager.tsx` (303, unrouted: `App.tsx` routes `/files` to `FileBrowser`). That is 1,332 lines. **Imported only by their own tests** (knip `--production`): `dedup/BulkActionBar.tsx` (173, handed over by 03), `filemanager/ImportPathCard.tsx` (204), `pages/Library.metadata.ts` (42), 419 lines plus 194 test lines. | knip output, [appendix D](01-legacy-and-dead-code/D-frontend.md). `tests/e2e/global-setup.ts` is a knip false positive (Playwright loads it). | high | −1,751 lines. |
| D7 | **35 `api.ts` client functions have no caller**, 458 lines. Among them: `getSystemLogs`, `listSessions`, `revokeSession`, `previewRename`, `applyRename`, `getBookUserTags`/`set`/`add`/`remove`, `batchApplyCandidates`, `batchRejectCandidates`, `batchUnrejectCandidates`, `getMetadataResults`, `getAnnouncements`, and the four handed over by 03 (`triggerDedupRefresh`, `requestAIAuthorReview`, `applyAIAuthorReview`, `getReconcilePreview`). Also unused: `readingApi` (`getBookPosition`, `setBookPosition`, `listByStatus`) and `playlistApi.reorderPlaylist`. | knip; the TS-compiler span script confirms 0 in-file references | high | −458+ lines. |
| D8 | **93 routes have no caller in the places searched**: non-test `web/src`, `cmd/`, `scripts/` and `.claude/` (Go clients under `internal/` were not searched). **72** remain after setting aside 13 with known outside callers and the 8 dedup routes in P72. The 13 are: the fingerprint worker routes (the Mac worker reaches them through `internal/fingerprint/workerclient`), the OAuth callback, accept-invite and `/admin/debug/*` (curl tooling). The dedup routes that 03 handed over are taken by P11. The route table was dumped from a test server, so routes behind config or build tags are missing; the list is a floor. | [appendix C](01-legacy-and-dead-code/C-routes-without-in-repo-caller.md) | medium | Each route needs an AudioBooth/curl check (Q6). **Not counted in the 93:** routes whose only frontend reference is an unused `api.ts` function (D7). These are the strongest candidates, because both ends are unused: `/audiobooks/:id/rename/{preview,apply}`, `/system/logs`, `/audiobooks/:id/user-tags*`, `/metadata/batch-{apply,reject,unreject}-candidates`, `/auth/sessions*`. |
| D9 | **Dead config.** `vite.config.ts:90-104` `test:` block: `vitest.config.ts` takes priority and does not `mergeConfig` it, so its coverage thresholds (15/10/15/15) never apply; the real ones are 25/20/20/30. | Vitest config resolution (handed over by 06) | high | −16 lines. Removing it also removes a misleading second set of thresholds. |

### 2.3 Useless logic (runs, changes nothing)

| ID | Finding | Evidence | Conf. | Impact |
|---|---|---|---|---|
| U1 | **The v1 reaper does a full keyspace scan every minute and can never act.** The ticker at `server_lifecycle.go:449-464` calls `failStaleOperations`, which calls `GetRecentOperations(500)`. That function (`pebble_store_operations.go:63-91`) unmarshals **every** `operation:` row, sorts them all, then truncates. That is 1,440 full scans a day. v1 has had no writer since 2026-08-23, and its write side, `UpdateOperationError`, is a v1 read-modify-write. | `sed -n 449,464p internal/server/server_lifecycle.go`; `sed -n 63,91p internal/database/pebble_store_operations.go`; `grep -rn '\.CreateOperation(' internal` finds only comments | high | Delete the ticker and `failStaleOperations`. `operation_timeout_minutes` stays while `/operations/stale` (`handler.go:416`) reads it (05). |
| U2 | **The pause-state response lists running ops from v1,** so `running_pausable` and `running_not_pausable` are always empty. The handler was added 2026-09-20 (`fadf003e9`), after v1 stopped being written. No frontend code reads the fields. | `sed -n 117,136p internal/server/handlers/operations/pause.go`; `git log --format=%h\ %ad -- internal/server/handlers/operations/pause.go`; `grep -rn running_pausable web/src` returns 0 | high | A bug. Repoint at `ListOperationsV2Since(time.Time{}, N)` filtered to non-terminal, or drop the fields (Q7). |
| U3 | **`organizer/service.go:286` writes `OrganizeParams{}` (an empty struct) into a side table that nothing reads.** This happens on every organize. | M4 | high | Delete with M4. |
| U4 | **Fourteen top-level `Config` fields are loaded and persisted but never read**: `CreateBackups`, `VerifyAfterWrite`, `EmbedCoverArt`, `MetadataReviewDefaultView`, `DefaultUserQuotaGB`, `MemoryLimitType`, `CacheSize`, `MemoryLimitPercent`, `MemoryLimitMB`, `LogFormat`, `EnableJsonLogging`, the empty `APIKeys struct{}`, plus `AuthRateLimitPerMinute` (validated at `config.go:3605`, but no limiter consults it) and `Language` (shown in `MetadataSettingsTab.tsx:411`, no backend reader). `Settings.tsx:510,590-604,622` renders several of them. This confirms the 2026-08-20 config audit at HEAD. | Typed field census: 0 selector reads outside `internal/config`, and inside it only load, persist or validate (see the list in [appendix E](01-legacy-and-dead-code/E-config-fields.md)) | high | The UI promises control it doesn't have. Wire or remove each one (Q8). |
| U5 | **A dead tag-write path** next to the live one: `metadata/enhanced.go:525,532` `WriteM4BCustomTags` and `writeM4BCustomTagsWithFFmpeg` (90 lines), plus `tagger.updateFileTags` and `tagger.WriteImageInPlace`. | deadcode | high | Delete. That narrows the surface the "tag-write gotchas" warn about. |
| U6 | **A superseded unguarded retire**: `plugins/maintenance/retire_into.go:68 retireInto` is unreachable now that callers go through the guarded wrapper (`ba46f13b8`). | deadcode | high | Delete, so the unguarded path can't be called again by mistake. |
| U7 | **`scanner.preserveExistingFields`** (104 lines) is dead. It also never worked: its last caller (removed in `7f5add8c0`) preserved fields into `dbBook` and then wrote `existingByOrgID`. Its removal therefore lost nothing. | `git show 7f5add8c0 -- internal/scanner/scanner.go \| grep -n preserveExistingFields` | high | Delete. |
| U8 | **The Users page ignores failures**: `pages/Users.tsx:82,90` `await fetch(...deactivate/reactivate)` never checks `resp.ok`, so a 403 or 500 looks like success. | `sed -n 78,92p web/src/pages/Users.tsx` | high | Fix with M8. |

Checked and **not** findings:
- `metadata_fetch_cache` is hit through `CachedMetadataForProvider` and `GetCachedMetadataFetchWithMaxAge`; only the old `GetCachedMetadataFetch` variant is dead.
- `revert_metadata_fetch.go:79` reads v2 first. It is a history bridge, not the #3103 failure class.
- staticcheck `SA4*`, `SA9003` and `U1000` find only 3 empty branches (`itunes/plist_parser.go:430`, `handlers/audiobooks/handler_metadata.go:185`) and 1 unused method (`repairs/owner.go:139`).
- The `/discovery/import` and `/deluge/discover/import` routes both exist. They are different handlers, not a broken button.

## 3. Proposed specification

**S1. Deletion tiers.** Each item is deleted only in the tier its evidence supports.

| Tier | Rule | Measured lines |
|---|---|---|
| 1 Safe now | Unreachable from every main and every route; superseded by shipped code; trial build green or a self-contained file | Go 2,449: 2,339 net file-lines from the build-verified trial deletion (M1 + D4 + the three M7 server files; the trial's 3,292 minus the 953 lines of `internal/download`), plus D3 72 and U1 38 (22 function + 16 ticker). TS 2,209: D6 1,751 + D7 458. Plus D9 16. U5–U7 are counted in tier 2. |
| 2 Per-item check | deadcode category D, minus test seams and minus the SQLite overlap; one PR per package ([appendix F](01-legacy-and-dead-code/F-tier2-pr-file-lists.md)), each func re-checked for reflection and JSON use | Go 3,162 |
| 3 Owner decision | Unwired features (D5, D2) and the SQLite backend (M2) | Go 2,161 func-lines, plus 5,181 (SQLite) |
| see 03 | `dedup.MergeBooks` cluster (705 lines) | 705 |

The total is **15,883** production lines (4,674 + 3,162 + 7,342 + 705); [appendix A](01-legacy-and-dead-code/A-method-and-totals.md) has the definition. About 6,360 test lines go with them: 5,373 SQLite, 797 download, 194 TS.

**S2. Guardrails that stop the debt coming back.**
- Add `deadcode` to `make ci` as a **ratchet**:
  - fail when the count of unreachable production funcs goes up;
  - keep a checked-in baseline at `scripts/deadcode-baseline.txt`;
  - exclude test-support paths.
- Add `knip --production` as a non-blocking report, then as a ratchet.
- Add a source-policy test, like the #3103 one, that forbids new references to the v1 `operation:` methods outside an allow-list.

**S3. Activity backend.**
- First flip the code default to `pebble` (`config.go:1077-1084` doc and default, `register.go:86-95`), so the conf file is no longer load-bearing.
- Then delete the NutsDB stack (M1).
- Delete the SQLite backend only if the owner chooses to (Q2).

**S4. Ops v1.** Delete only the parts that are inert or buggy today (U1, U2, D3, M4). The history bridges, the retention purge and the table itself stay for 05 and 04.

**S5. Frontend transport.** Every request goes through `apiFetch` or a `services/*` function. The ESLint rule `no-restricted-globals: fetch` applies outside `web/src/services` and `utils/apiFetch.ts`.

## 4. Implementation plan

Each PR is independent unless stated otherwise. Every PR that removes a store method must also touch `internal/database/mock_store.go` and the regenerated `internal/database/mocks/mock_store.go`, and narrow the interfaces that declare the method.

| PR | Title | Files | Tests | Rollback | Size |
|---|---|---|---|---|---|
| P1 | fix(activity): default ActivityBackend to pebble | `internal/config/config.go`, `internal/activity/register.go`, `internal/activity/service_test.go` (new cases), `internal/config/config_test.go`, `docs/architecture.md`, `docs/reference/config-api-shape.md` | unit test for the empty, `pebble` and `sqlite` values | revert; prod conf already says pebble | S |
| P2 | refactor(database): delete NutsDB activity stack | delete `internal/database/{nuts_activity_store,nuts_metrics_store,dual_write_activity_store,activity_store_instrumented,pebble_activity_backfill}.go`; new `internal/database/activity_tiers.go` (`actTiers`, `actCompactableTiers`, `matchesFilter`); edit `sql_activity_migrating_store.go:313-322`, `activity_partial_query.go:40`; port tests `internal/activity/{service,writer,writer_attrs}_test.go`, `internal/server/{activity_handlers,activity_integration}_test.go`, `internal/database/{activity_compact,pebble_activity_store,pebble_activity_filter_index,activity_summarize_grouping,activity_tag_aliases,store_coverage}_test.go`; `go.mod`/`go.sum` (`go mod tidy` drops nutsdb) | `go build ./... && go vet ./...` (the trial is green for prod); ported tests run on `PebbleActivityStore` | revert | M |
| P3 | fix(operations): delete v1 reaper; pause-state reads v2 | `internal/server/server_lifecycle.go` (449-464, 1706-1727), `internal/server/handlers/operations/pause.go`, `pause_test.go`, `handler.go` (delete `CancelOperation` 162-226, `resolveScheduler` 141), `internal/server/handlers/operations/handler_test.go`, `internal/server/handlers_integration_test.go` | test: a running v2 op appears in `running_pausable` | revert | S |
| P4 | refactor(operations): delete the opstate params side table | `internal/operations/state.go`, `internal/organizer/service.go:286`, `internal/itunes/service/importer.go:359` (read-only change: removes a DB side write, not an iTunes write), `internal/database/pebble_store_operations.go` (`SaveOperationParams`, `GetOperationParams`), `iface_ops.go:34`, `internal/server/server_ops_store.go:227`, both mock files | `go build`; existing organize and import tests | revert; rows left on disk are harmless | S |
| P5 | refactor(server): delete validators and server shims | delete `internal/server/{validators,file_move,pipeline_checkpoint,deluge_importer_adapter}.go` and their tests; `internal/server/wire_handlers.go:732,746` call `audiobooks.New*` directly; trim `internal/server/audiobooks_compat.go`; update `docs/compat-surfaces.md` | build + vet | revert | S |
| P6 | refactor: delete dead tag-write, retire and scanner helpers (U5–U7) | `internal/metadata/enhanced.go`, `internal/tagger/{tagger,safe_write}.go`, `internal/plugins/maintenance/retire_into.go`, `internal/scanner/scanner.go` and their tests | build + vet + package tests | revert | S |
| P7 | chore(web): delete unused files, exports and dead vite test config | the 12 files in D6 and their 3 tests; `web/src/services/{api,readingApi,playlistApi}.ts`; `web/vite.config.ts` | `npm run build`, `vitest run`, knip clean for those entries | revert | M |
| P8 | fix(web): route every request through apiFetch | `web/src/pages/{Users,Setup,Login,BookDetail,TrashedVersions}.tsx`, `web/src/App.tsx`, `web/src/components/{ChangeLog,AnnouncementBanner}.tsx`, `web/src/components/settings/{DelugeSettingsTab,PluginsTab,ITunesTransfer}.tsx`, `web/src/components/audiobooks/VersionsPanel.tsx`, `web/src/services/{versionApi,playlistApi,fileOpsApi}.ts`, `web/eslint.config.mjs` | vitest per page: a login-redirect response surfaces an error | revert | M |
| P9 | ci: deadcode and knip ratchet | `Makefile`, `scripts/deadcode-ratchet.py`, `scripts/deadcode-baseline.txt`, `.github/workflows/ci.yml` | the ratchet fails on an injected dead func | remove the target | S |
| P10..P71 | refactor(<pkg>): tier-2 dead funcs, one PR per package (62 packages) | exact files per PR in [appendix F](01-legacy-and-dead-code/F-tier2-pr-file-lists.md) | build, vet, package tests | revert | S each; `internal/database` and `internal/server` are M |
| P72 | refactor(server): drop dead dedup routes and verb aliases (taken from 03 §6) | `internal/server/wire_dedup_routes.go` (lines 34, 42, 44, 46, 53, 55, 65, 70, 71, 73, 76, 77, 78, 101, 117, 118), `internal/server/wire_media_routes.go:48-49`; handlers that lose their last route: `internal/server/handlers/dedup/*` (`PurgeAcoustIDConflicts`, `TriggerBookSignatureScan`, `TriggerDedupRefresh`, `PurgeLegacyFPCandidates`, `TriggerEmbedAsync`, `TriggerLSHIndexBuild`, `EmbReeencode`), `internal/server/handlers/duplicates/*` (`SeriesNormalizePreview`, `SeriesNormalize`), `internal/server/handlers/ai.go` (`ReviewDuplicateAuthors`, `ApplyAuthorReview`); tests `internal/server/dedup_link_reject_alias_route_test.go`, `internal/server/duplicates_ops_reroute_test.go`, `internal/server/handlers/{dedup,duplicates}/handler_test.go`, `internal/server/handlers/ai_test.go` | build, vet; the route-table test asserts the aliases are gone | revert | M; gated on Q6 (curl users). The ops behind them stay; pruning ops is 04's call |
| P73 | refactor(dedup): delete the dead category C cluster (coordinator-assigned; was "see 03") | `internal/dedup/book_dedup.go` (`MergeBooks`, `guardKeeperAudioRoute`, `retireMergedLoser`, `bookAudioPaths`, `TransferITunesMetadataFirstWin`), `internal/dedup/author.go`, `internal/dedup/engine.go` (4 funcs), `internal/dedup/collectors_embedding.go`, `internal/dedup/collectors_metadata.go`, `internal/maintenance/jobs/dedup_books.go` (`ddMergeDuplicateBook`, also listed in P36: do it here and drop it from P36), tests that only exercise them | **Precondition:** show with a test that the live merge path (`internal/merge`) refuses the same cases `guardKeeperAudioRoute` and `retireMergedLoser` refused: a keeper with no audio route, and an unguarded loser retire. If either guard has no live equivalent, port the guard before deleting. Then build, vet, `internal/dedup` tests | revert | M |
| P74 | refactor(operations): retire the v1 `operation:` keyspace and its last readers (coordinator-assigned) | history bridges `internal/server/maintenance_fixups.go:552`, `internal/metabatch/fetch_ops_index.go:165`, `internal/maintenance/jobs/revert_metadata_fetch.go:79`; `internal/sysinfo/service.go:324`; the v1 retention purge `internal/maintenance/jobs/retention_and_hygiene.go:170-210,281`; `internal/operations/registry/legacy_op_status.go`, `internal/operations/registry/legacy_backfill.go`, `internal/server/legacy_backfill_op.go` (op `operations.backfill-legacy-status` → `retiredOpIDs`); `internal/database/pebble_store_operations.go`, `iface_ops.go`, both mock files; `internal/server/testdata/op_ids.golden` unchanged | a source-policy test that no non-test code calls a v1 method; `TestOpIDs_NoRenameWithoutAlias` | revert; the rows stay on disk until a separate purge | M; **gated on owner decision D17 in 08 §7** (pre-2026-08-23 op history stops being readable in the UI) and after 05 PR 4 |
| P-Q | owner-gated: SQLite backend (Q2), `internal/download` (Q3), unwired features (Q5), rename routes (Q4), config fields (Q8), routes (Q6) | as each answer decides | | | M–L |

Order:
- P1 before P2.
- P7 before P8: both touch `web/src/services/playlistApi.ts`.
- P3 before P72: both touch `internal/server` handler tests.
- P4 before the `internal/database` and `internal/operations` tier-2 PRs, because they share files.
- P9 last, so its baseline captures the cleaned state.
- P73 after 03 PR 11 (both touch dedup tests), and after 02 PR 11/12 rebase on it (`engine.go`).
- P74 after P3, P4 and 05 PR 4.

No other pair shares a file.

## 5. Risks and what must not break

- **`internal/writeback/`** is untouched; its 6 unreachable funcs are excluded from every total.
- **iTunes.** `itunes/service/importer.go:359` is changed only to drop a write to an unread Pebble side row. No iTunes write, removal or rebuild is involved. The dead iTunes export code (D5) is not proposed for deletion.
- **Activity history.** P2 must keep `matchesFilter` and `actTiers` byte-identical; the Pebble query path depends on them. P1 changes behavior only for an install with no `activity_backend` set. Prod already sets `pebble`.
- **Test seams.** `NewPebbleStoreInMemory`, `SetClock`, `SetSleep`, `setRowScanFault*` and the like look dead to deadcode but are used by tests in other packages. They are excluded from tier 2.
- **External callers.** AudioBooth and curl skills may call routes that look unused (D8), so no route is deleted without Q6. `/books/:id/position` is the most likely AudioBooth caller.
- **deadcode undercounts.** Exported methods on types that escape to `any` look reachable (D3 was found only by route census), so the totals are a floor.

## 6. Dependencies on other workstreams

- **03:**
  - Ownership settled: this doc takes the dead dedup routes and verb aliases from 03 §6 (P72) and the four `api.ts` wrappers and two components (P7). 03 keeps only what dies with the page.
  - The `dedup.MergeBooks` cluster (`book_dedup.go:381-757`, 705 func-lines) and `maintenance/jobs/dedup_books.go:948 ddMergeDuplicateBook`. Before deleting, 03 must confirm that the live merge path has the same guards (`guardKeeperAudioRoute`, `retireMergedLoser`).
  - `pages/BookDedup.tsx` and `DedupLabels.tsx`, which die with the page.
  - Folded in here, because they are dead independently of the page: `FingerprintVisualsColumn.tsx`, `BulkActionBar.tsx`, and the four `api.ts` wrappers.
- **04:** the v1 retention purge (`retention_and_hygiene.go:170-210`), the `operations.backfill-legacy-status` op (`registry/legacy_backfill.go`), and whether `/operations/stale` and `/operations/clear-stale` survive.
- **05:**
  - ~~The rest of the v1 retirement~~ **Coordinator: now 01 P74.** 05 §6 handed the same items back to 01, so 01 owns them.
  - The sequential `RunItems` calls (M9).
  - The ratchet in P9 should also cover the new SDK.
- **06:** `vite.config.ts` dead `test:` block (D9), handed over by 06 and verified here.
- **07:** the `Book.FilePath` → `book_file` design (M6) and the `UpdateBook` row-version guard (M5).
- **02:** the windowed fingerprint and transcript classifiers (D5) are identification signals. If 02 wires them, they move out of tier 3.

## 7. Open questions for the owner

| # | Question | Recommended answer |
|---|---|---|
| Q1 | Flip the activity-backend code default to `pebble` now? | **Yes.** Prod already runs it, and the conf file should not be the only thing keeping prod on it. |
| Q2 | Delete the SQLite activity backend (5,181 prod and 5,373 test lines)? The 20 GB `activity.sqlite` stays your call either way. | **Yes, after Q1 has soaked one release.** Keep a tagged commit to restore from. The asymmetric-rollback note says returning to SQLite already needs a catch-up op that doesn't exist. |
| Q3 | Delete `internal/download` (the qBittorrent and SABnzbd clients and their config keys), which was built but never wired? | **Yes.** Deluge is the only live client and lives in `internal/deluge`. Restoring the code from git is cheap if the feature is ever wanted. |
| Q4 | `/audiobooks/:id/rename/{preview,apply}` has no client, and its apply path has the last unconverted whole-row write. Delete it or convert it? | **Delete it.** Organize and write-back cover renames. Converting it means keeping an unused route alive. |
| Q5 | The unwired features (windowed fingerprint similarity, intro classification, iTunes XML export, Prometheus handler, version swap, cover embed): wire or delete? | **Decide per feature with 02 and 07.** Default to delete for version swap and the cover-embed pair, keep the fingerprint and transcript signals for 02, and leave the iTunes export alone. |
| Q6 | Of the 72 triage routes with no in-repo caller, which does AudioBooth or your tooling use? | **Mark the ones you use. Delete the rest in one PR per handler group,** with a 410 stub for one release. |
| Q7 | Pause-state `running_*` fields: repoint at v2 or drop them? | **Repoint.** The pause banner will need them, and the fix is S. |
| Q8 | The fourteen Config fields the Settings page shows but nothing reads: wire each one or remove it? | **Remove the fields and UI for the memory-limit, log-format, `language`, `auth_rate_limit_per_minute` and `metadata_review_default_view` ones. Ask about `create_backups`, `verify_after_write` and `embed_cover_art`**, because users expect those three to work. |
| Q9 | Add `deadcode` to CI as a ratchet? | **Yes.** A full run takes about 11 s wall time (measured on the Mac) and stops this list from growing back. |
