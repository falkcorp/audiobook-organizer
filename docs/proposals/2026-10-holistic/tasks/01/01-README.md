<!-- file: docs/proposals/2026-10-holistic/tasks/01/01-README.md -->
<!-- version: 1.0.4 -->
<!-- guid: ae2155fb-ede4-4669-bc91-6c7b6c927cdd -->
<!-- last-edited: 2026-10-09 -->

# Workstream 01 task briefs: legacy and dead code

Source: [`01-legacy-and-dead-code.md`](../../01-legacy-and-dead-code.md) (HEAD measured `f7211eb39`, briefs verified against `93a9b745f`), sequenced by [`08-integrated-roadmap.md`](../../08-integrated-roadmap.md) sections 4 and 5, owner answers in [`09-owner-decisions.md`](../../09-owner-decisions.md). Template: [`00-TEMPLATE.md`](../00-TEMPLATE.md).

Briefs: **40** (opus: 2, sonnet: 38). Wave 0: 6, wave 1: 8, wave 2: 15, freeze window F: 11.

## Index

| Brief | Title | Wave | Model | Size | Depends on |
|---|---|---|---|---|---|
| [01-P1](01-P1.md) | Default ActivityBackend to pebble | 0 | sonnet | S | 07 C1 (CI green on main; 08 section 5 row 0.1) |
| [01-P3](01-P3.md) | Delete the v1 stale-operation reaper; pause state reads v2 | 0 | sonnet | S | 07 C1 |
| [01-P4](01-P4.md) | Delete the opstate params side table | 0 | sonnet | S | none |
| [01-P5](01-P5.md) | Delete validators and the server compat shims | 0 | sonnet | S | none |
| [01-P6](01-P6.md) | Delete dead tag-write, retire, scanner and metrics helpers | 0 | sonnet | M | none (merge before 01 P79b and P79c, which edit `internal/tagger/safe_write.go`) |
| [01-P79a](01-P79a.md) | Honour embed_cover_art and default it on | 0 | sonnet | S | 07 C1 |
| [01-P2](01-P2.md) | Delete the NutsDB activity stack | 1 | sonnet | M | 01 P1 (merged) |
| [01-P7](01-P7.md) | Delete unused web files, exports and the dead vite test config | 1 | sonnet | M | 07 C1 |
| [01-P8](01-P8.md) | Route every web request through apiFetch | 1 | sonnet | M | 01 P7 (both touch `web/src/services/playlistApi.ts`) |
| [01-P9](01-P9.md) | CI ratchet for dead Go code (deadcode) and a knip report | 1 | sonnet | S | 07 C3 (ci.yml order: C2 -> C3 -> 06 P2 -> 01 P9 -> 07 G4); the tier-1 PRs P2, P4, P5, P6, P7 merged first so the baseline captures the cleaned state; 07 C1 (ratchets are one-way) |
| [01-P76](01-P76.md) | Delete internal/download and its config | 2 | sonnet | S | 07 C1; 01 P79a (config.go order: P79a -> P76); 10 PR 0–2 (D68) |
| [01-P77](01-P77.md) | Delete the rename preview and apply routes | 1 | sonnet | S | 01 P5 (both edit `wire_handlers.go` / `audiobooks_compat.go`); 01 P7 and 05 PR 1 on `api.ts` (R16 order: P7 -> 05 PR 1 -> P77; 03 PR 3 is wave 2 and rebases on this) |
| [01-P79b](01-P79b.md) | Honour create_backups on tag writes | 1 | opus | S | 04 P3a (the scheduled cleanup must read `backup_retention_days` first, so the new `.bak-*` siblings are swept); 01 P6 (edits `internal/tagger/safe_write.go` first); 01 P79a (config doc order) |
| [01-P79c](01-P79c.md) | Honour verify_after_write on tag writes | 1 | sonnet | S | 01 P6 (edits `tagger/safe_write.go` first); 01 P79a; 01 P79b (same file, merge in that order) |
| [01-P81a](01-P81a.md) | Retire the wipe route (first of the P81 series) | 1 | sonnet | S | 01 P3 and 07 R4 (server_lifecycle.go order: 01 P3 -> 07 R2 -> R3 -> R4 -> 01 P81 wipe) |
| [01-P72](01-P72.md) | Drop dead dedup routes and verb aliases | 2 | sonnet | M | 01 P3 (handler tests), 01 P7 (the api.ts wrappers for these routes are gone), 03 PR 1-3 as sequenced in 08; the owner must have confirmed no curl user (Q6) |
| [01-P73](01-P73.md) | Delete the dead dedup category C cluster (MergeBooks and friends) | 2 | sonnet | M | 03 PR 11 (both touch dedup tests); 02 PR 11/12 rebase on this PR (`engine.go`); the guard-parity test below must pass first |
| [01-P75](01-P75.md) | Delete the SQLite activity backend | 2 | opus | M | 01 P1 shipped for at least one release (D11); 01 P2 (go.mod order: P2 -> 11 PR 2 -> 05 PR 8 -> P75); config.go order: 01 P79a -> P76 -> 04 P10 -> 02 PR 10 -> P80 -> 02 PR 19 -> P75 |
| [01-P80](01-P80.md) | Remove the 11 unread Settings fields and their UI | 2 | sonnet | M | 01 P76 and 01 P79a (config.go order: P79a -> P76 -> 04 P10 -> 02 PR 10 -> P80); 01 P79b/P79c for the config doc |
| [01-P81b](01-P81b.md) | Retire the reading-state routes (books/:id aliases and status repair) | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring files), 01 P3; AND the owner has marked appendix C |
| [01-P81c](01-P81c.md) | Retire the collections and playlist-export routes | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring files), 01 P3; AND the owner has marked appendix C |
| [01-P81d](01-P81d.md) | Retire the audiobook alternative-titles, path-history and rescan routes | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring files), 01 P3; AND the owner has marked appendix C |
| [01-P81e](01-P81e.md) | Retire the narrator, work-stats and entity-tag routes | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring files), 01 P3; AND the owner has marked appendix C |
| [01-P81f](01-P81f.md) | Retire the provider-throttle and metadata-fields routes | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring files), 01 P3; AND the owner has marked appendix C |
| [01-P81g](01-P81g.md) | Retire the cache, activity-maintenance, system-log and diagnostics routes | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring files), 01 P3; AND the owner has marked appendix C |
| [01-P81h](01-P81h.md) | Retire the merge-journal routes and duplicate verb aliases | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring files), 01 P3; AND the owner has marked appendix C |
| [01-P81i](01-P81i.md) | Retire the server_lifecycle-owned orphan routes | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring files), 01 P3; AND the owner has marked appendix C |
| [01-P81j](01-P81j.md) | Retire the catalog, op-defs, AI-status, tools and review-replay routes | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring files), 01 P3; AND the owner has marked appendix C |
| [01-P81k](01-P81k.md) | Retire the Deluge discovery, iTunes diagnostics, API-key rotate and version-alias routes | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring files), 01 P3; AND the owner has marked appendix C |
| [01-T1](01-T1.md) | Delete unreachable functions in internal/database | F | sonnet | M | 01 P4 (opstate params), 05 PR 4, 01 P2 and 01 P75 where they delete database files; D18/P9 ratchet baseline regenerated after |
| [01-T2](01-T2.md) | Delete unreachable functions in internal/server and its handler packages | F | sonnet | M | 01 P3, 01 P72, 03 PR 12; all 01 P81 groups that edit the wiring files |
| [01-T3a](01-T3a.md) | Delete unreachable functions in metadata, providerhttp, openlibrary and authority | F | sonnet | S | 02 PR 6 (question-keyed provider cache); 01 P6 (enhanced.go) |
| [01-T3b](01-T3b.md) | Delete unreachable functions in metafetch, metabatch, catalog, matcher, personname and franchise | F | sonnet | S | 02 PR 7b and 02 PR 9a |
| [01-T4a](01-T4a.md) | Delete unreachable functions in scanner, organizer, undo and backup | F | sonnet | S | 01 P77 (rename service code), 01 P6 (scanner.go) |
| [01-T4b](01-T4b.md) | Delete unreachable functions in fileops, audioutil, tagger, mediainfo, audioext and pathutil | F | sonnet | S | 01 P6, 01 P79b and 01 P79c (`safe_write.go`, `write_tags_safe.go`) |
| [01-T5a](01-T5a.md) | Delete unreachable functions in operations, freshness, registry, opmode and scheduler | F | sonnet | S | 01 P4 and the 04 scheduler PRs (04 P1, P4a-P4f, P9, P12) |
| [01-T5b](01-T5b.md) | Delete unreachable functions in plugins/maintenance, repairs, applygate and applycap | F | sonnet | S | 02 PR 8, 02 PR 9a |
| [01-T6a](01-T6a.md) | Delete unreachable functions in errhandling, logger, httputil, util, config, security, sysinfo, appdirs and policy | F | sonnet | M | 01 P80 (`internal/config`), 07 C1 (the `logger` slog ratchet entry) |
| [01-T6b](01-T6b.md) | Delete unreachable functions in cache, searchcache, search, realtime, serviceregistry, syncapi/progress, activity, merge, versionprimary and metrics | F | sonnet | S | 01 P75 (`internal/activity`); `internal/metrics/pipeline_metrics.go` function must be deleted BEFORE 11 PR 6 |
| [01-T7](01-T7.md) | Delete unreachable functions in ai, aidispatch, fingerprint, diagnosis and audiobooks | F | sonnet | S | 11 PR 3 (`aidispatch` instruments), 02 PR 1 (`audiobooks` benches) |

## Not briefed (wave 3 or 4)

| Item | Wave | Where it is specified |
|---|---|---|
| 01 P74: retire the v1 `operation:` keyspace and its last readers | 4 (after 05 PR 4; gated on D17) | `01-legacy-and-dead-code.md` section 4 row P74 |
| 01 P81 follow-up: remove the 410 stubs one release after each P81 group (a single PR) | 4 | `01-legacy-and-dead-code.md` section 4 row P81, last sentence; 08 section 5 "Wave 4" |
| P10 to P71 (the 62 one-package tier-2 rows) | superseded by T1 to T7 above | `01-legacy-and-dead-code/F-tier2-pr-file-lists.md`; 08 section 5 "Freeze window F" |
| P36 | folded into P73 | `01-legacy-and-dead-code.md` section 4 row P73 |

## Verification notes (what the briefs corrected against HEAD)

These are places where a brief departs from the 01 doc because the code at HEAD said otherwise. The coordinator should fold them back into 01.

- **P3**: `collectStaleOperations` is still passed to the operations handler (`wire_handlers.go:251`) for `/operations/stale`; the brief deletes only the ticker and `failStaleOperations`. The pause handler holds a narrow v1 store interface, so the brief adds `ListOperationsV2Since` to it and regenerates a mock.
- **P4**: the iTunes importer tests carry 11 `SaveOperationParams` expectations that must go; `OperationParamsWriter` is embedded in the importer's store interface.
- **P7**: four of the twelve file paths in 01 D6 are wrong (`FingerprintVisualsColumn.tsx` is under `components/`, not `components/dedup/`; `InlineEditField`, `MetadataDiffTable`, `TagEditor` are under `components/audiobooks/`). `previewRename`/`applyRename` are left to P77 so `api.ts` is edited once per owner.
- **P8**: 23 raw `fetch` sites outside `services/` at HEAD (01 says 22), plus 3 inside `services/` (`versionApi.ts`, `fileOpsApi.ts`, `playlistApi.ts`). Three sites run before a session exists (`App.tsx`, `Login.tsx`, `Setup.tsx`) and need an explicit auth-redirect decision.
- **P75**: the 01 file list is incomplete. Live code names the SQL types: `activity/relocate.go`, `database/activity_storer.go:50`, `database/sql_dialect.go`, comments and fan-out wrappers in `pebble_activity_store.go`, `plugins/maintenance/cleanup.go`, `compact_activity_log.go`, `server_maintenance_deps.go`. `ActivityDBMoveOnChange` also dies. The brief is opus ("discover and sweep"). `modernc.org/sqlite` has a single importer, so it leaves `go.mod`.
- **P76**: the `download_client` config is also woven through `update_service.go` (secret masking and restore) and `protected_fields.go`, and the six-secret test table loses three rows.
- **P77**: `organizer.RenameService` is still built by `organizer/preview.go:240`, so the type may survive; the brief makes per-function verdicts instead of deleting `rename.go` wholesale.
- **P79a**: `embed_cover_art` has TWO defaults to flip (`viper.SetDefault` and the struct literal). `docs/reference/config-api-shape.md` does not name any of the three toggles.
- **P79b**: the 01 text says "rename the original to `.bak-<ts>`"; the brief copies instead (`CopyFileInto`, fsynced) and then renames the temp in atomically, so there is never a moment when the file does not exist. `tagger.hashOptions` returns early when `HashStore` is nil, so the flag must be applied on every return path.
- **P80**: line numbers in 01 are stale and the brief locates by symbol; the top-level `Language` field must not be confused with the catalog provider `Language` fields; `auth_rate_limit_per_minute` carries a "sign-in brute-force limit" description in `protected_fields.go`, so the brief asks the agent to report what really limits sign-in.
- **P81**: 13 of the 72 triage routes are second registrations of handlers that stay live under another path (`/books/:id/...` versus `/audiobooks/:id/...`, `/rescan` versus `/reconcile-files`, `duplicates/merge|dismiss` versus `link|reject`). For those only the route goes, never the handler. `GET /maintenance/repair-missing-files/:id` is excluded (04 D27). The wipe route's PR also removes `internal/server/mock_prefix_wiper_test.go` and an `opmode/guard_test.go` entry that 01 does not list. The 72 routes split into 10 handler-group briefs (P81b to P81k) plus the wipe brief P81a; each carries a "mark before retiring" gate.
- **T1 to T7 versus P3/P4/P6/P73**: 13 of the 279 tier-2 functions (344 lines) are also deleted by P3, P4, P6 and P73 (`resolveScheduler`, `LoadParams`, `LoadRawParams`, `SaveRawParams`, `preserveExistingFields`, `retireInto`, `updateFileTags` and its three helpers, `WriteImageInPlace`, `WriteM4BCustomTags`, `writeM4BCustomTagsWithFFmpeg`, and `ddMergeDuplicateBook`). Beyond that overlap, 40 functions (358 lines) are HELD BACK in the T briefs because they have callers the deadcode run did not count as roots: a cgo-tagged file (`metadata.BookFileHashOptions`), test seams used by other packages' tests (`SetMetadataExtractor`, `KnownProviders`, `HasOverride`, and 36 more, each named with its caller in the brief). The T briefs therefore delete **225 functions / 2,430 lines**, not 08's 278 / 3,132. `golang.org/x/tools` is not in `go.mod`; the briefs install `deadcode@v0.51.0` (the version appendix A used) into a scratch GOBIN.
