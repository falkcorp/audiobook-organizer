<!-- file: docs/proposals/2026-10-holistic/tasks/04/04-README.md -->
<!-- version: 1.0.1 -->
<!-- guid: f21a806b-d540-4258-8215-42ad144e2775 -->
<!-- last-edited: 2026-10-09 -->

# 04: Operations census task briefs, index

One brief per PR in `docs/proposals/2026-10-holistic/04-operations-census.md` §4 that is scheduled for waves 0 and 1 of `08-integrated-roadmap.md` §5. Format: `../00-TEMPLATE.md`. Briefs are listed in **merge order**, which is the order the shared files require (`08` §4: `internal/scheduler/tasks.go` is P1, P4a, P9, P12; `internal/plugins/maintenance/plugin.go` is P2, P5, P12; `internal/server/server_lifecycle.go` is P6, P7, P13). Develop in parallel if you like; merge one at a time. P8 (activity SQLite migration as an op) is not here: it is dropped (D11, 01 P75).

| # | Brief | Title | Wave | Model | Size | Depends on |
|---|---|---|---|---|---|---|
| 1 | [`04-P3a.md`](04-P3a.md) | Backup cleanup reads its own backup_retention_days; D5 prod pre-check | 0 | sonnet | M | none in code; owner pre-check |
| 2 | [`04-P2.md`](04-P2.md) | Retire the two ISBN-enrichment stubs | 0 | sonnet | S | P3a |
| 3 | [`04-P1.md`](04-P1.md) | Schedule file-integrity-check and orphan-book-files-cleanup; schedule-has-driver guard | 0 | sonnet | S | P2 |
| 4 | [`04-P5.md`](04-P5.md) | Series twins: dedup.series-* survive | 1 | sonnet | S | P2, P1 |
| 5 | [`04-P4a.md`](04-P4a.md) | Merge temp-file, trash, tombstone, db-optimize twins | 1 | sonnet | M | P5, P1 |
| 6 | [`04-P4b.md`](04-P4b.md) | Merge purge-deleted twin; one runAutoPurgeSoftDeleted | 1 | sonnet | M | P4a |
| 7 | [`04-P4c.md`](04-P4c.md) | Merge metadata-refresh twin | 1 | sonnet | M | P4b |
| 8 | [`04-P4d.md`](04-P4d.md) | Merge resolve-production-authors twin | 1 | sonnet | M | P4c |
| 9 | [`04-P4e.md`](04-P4e.md) | Merge author-split-scan twin | 1 | sonnet | L | P4d |
| 10 | [`04-P4f.md`](04-P4f.md) | Merge cleanup-old-backups twin | 1 | sonnet | M | P3a, P4e |
| 11 | [`04-P3b.md`](04-P3b.md) | One shared .bak sweep helper; retire cleanup-backups job | 1 | sonnet | M | P4f |
| 12 | [`04-P11.md`](04-P11.md) | Concurrency on the five sequential RunItems calls plus AST lint | 1 | sonnet | S | P3b (order) |
| 13 | [`04-P14a.md`](04-P14a.md) | C1: author-dedup-scan retires, dedup.author-scan survives | 1 | sonnet | S | P11, P4a-P4f, P3b |
| 14 | [`04-P14b.md`](04-P14b.md) | C2 (D54): scheduler.dedup-llm-review retires, dedup.llm-review survives | 1 | sonnet | S | P14a |
| 15 | [`04-P14c.md`](04-P14c.md) | C3: maintenance.reconcile-scan retires, reconcile.scan survives | 1 | sonnet | M | P14b (gates 03 PR 9b) |
| 16 | [`04-P14d.md`](04-P14d.md) | C5: fix-book-file-paths job retires, mark-missing-files survives | 1 | sonnet | S | P14c |
| 17 | [`04-P14e.md`](04-P14e.md) | C6: repair-missing-files job retires, repoint and recover survive | 1 | sonnet | S | P14d |
| 18 | [`04-P14f.md`](04-P14f.md) | C7: bulk-fetch-metadata job retires, two survivors | 1 | sonnet | S | P14e |
| 19 | [`04-P6.md`](04-P6.md) | Boot goroutines become ops (six conversions), gated on readiness | 1 | opus | M | P14f; 07 R2 (R3/R4 preferred); 01 P81 for file order |
| 20 | [`04-P7.md`](04-P7.md) | opchange index ensure-mode op replaces the boot goroutine | 1 | opus | M | P6 |
| 21 | [`04-P9.md`](04-P9.md) | Label refinement chain becomes a parent op | 1 | sonnet | S | P7 |
| 22 | [`04-P12.md`](04-P12.md) | Delete maintenance.batch-poller (D26) | 1 | sonnet | S | P9, P2, P5 |
| 23 | [`04-P13.md`](04-P13.md) | Fold transcode temp ticker into temp-file-cleanup op | 1 | sonnet | S | P12, P4a, P7 |
| 24 | [`04-P10.md`](04-P10.md) | Dedup-on-import always via dedup.check-book (flag kept for soak) | 1 | sonnet | M | P13 |

**Counts.** 24 briefs: 22 sonnet, 2 opus (P6, P7). Sizes: 12 S, 11 M (P3a is M here; the census sized it S), 1 L (P4e).

P1 creates `internal/server/op_schedule_driver_test.go` and P5 creates `internal/server/op_twin_merge_test.go`; later briefs edit those files, so the briefs are valid only in the merge order above.

## Shared mechanics every twin and near-duplicate brief uses

- Op-id guard: `go test ./internal/server/... -run TestOpIDs -count=1` (`internal/server/op_id_aliases_test.go`; ledger `internal/server/testdata/op_ids.golden`, append-only). Also `-run TestWriteOps` for `internal/server/testdata/write_op_modes.golden` (loser lines are deleted; the file is not append-only).
- Alias resolves in a test: `renamedOpIDs` in `op_id_aliases_test.go` (checked by `TestOpIDs_EveryAliasResolves`), plus the shared table test `internal/server/op_twin_merge_test.go` that 04-P5 creates and every later merge appends to (alias resolves, survivor `Permissions`, `Timeout`, 403 for a caller holding only `scan.trigger`). Retirements (no alias) use `retiredOpIDs` and the table's retired half.
- Permission carried: `settings.manage` on every survivor of a `scheduler.*` pair (P4a to P4f) and on the survivors that declared none (P14b, P14d, P14e); the series, author-scan and metadata survivors already hold a stricter or equal permission and keep it.
- `TestOpIDs_CodeNeverNamesAnAlias` means a retargeted task, `taskV2DefIDs` entry or UI string must use the survivor ID.
- The D24 allow-list in `internal/server/op_schedule_driver_test.go` (added by P1) loses rows as twins merge.
- Soak: P14a to P14f (and P10) end with a separate follow-up line for the 2-week soak and flag or alias retirement (D27); none of those PRs retires anything.

## Where the code at HEAD disagrees with the census (read before dispatching)

1. **No `backup_retention_days` setting exists** (P3a). `Server.BackupRetentionDays()` returns `PurgeSoftDeletedAfterDays`, the same field the scheduled op reads, so census F4 ("the twin reads the right setting") is false at `93a9b745f`. D5's prod comparison cannot compare two values; P3a does the pre-check, reports what it finds, and adds the key with a fall-back so deploy changes nothing. The owner should confirm this reading of D5.
2. **`maintenance.purge-deleted` has no preview mode** (P4b). Both twins are class `no-mode`; the census's "survivor deletes nothing on `{}`" test cannot exist. P4b asserts the real valve (`PurgeSoftDeletedAfterDays <= 0`) and passes `ctx` through the single remaining body.
3. **`reconcile.scan` already saves results** (P14c). The real defect is that `recentReconcileScans` matches one literal def ID, which hides rows stored under the retired ID; P14c canonicalizes. The loser's 180-minute timeout is the larger, so the survivor goes to 3 h.
4. **The boot goroutines are not exact twins** (P6, P7). Each op is the force variant; boot wants the sentinel-gated variant (and the opchange boot path is a verify-then-rebuild, not the op's preview/rebuild). Both briefs add an explicit boot param instead of enqueueing the op unchanged.
5. **P3b does not widen the scheduled `.bak-*` deleter** to the retired job's suffix patterns (the job deleted with no age test). The capability is dropped and flagged for the owner.
6. **P10 has a stop condition**: `dedup.check-book` waits for `book_sig_v1`; if un-fingerprintable books would never be checked, the default stays false.
7. P1 also edits `internal/scheduler/scheduler.go` (`maintenanceOrder`), which the census file list omitted; a task missing from that list never runs in the window.
