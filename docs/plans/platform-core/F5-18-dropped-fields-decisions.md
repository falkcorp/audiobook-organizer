<!-- file: docs/plans/platform-core/F5-18-dropped-fields-decisions.md -->
<!-- version: 0.1.0 -->
<!-- guid: 2f6b8d41-7c93-4e05-b1a8-d49e3c70f5a2 -->
<!-- last-edited: 2026-11-21 -->

# F5-18f O20: what happens to each HEAD definition field the native kinds do not carry

Source of the question: `F5-18-prep-split-proposal.md` O20, `seams.d/F5-06.md:37`, `F5-10.md:30`,
`F5-11.md:50`, `F5-01.md:101`. Each row says what the spec already decides and where the field lives
natively. Nothing here changes code: every decision is either "drop" (the native definition correctly
carries nothing) or "move" to a place a later row already owns. Rows marked **default, owner asleep**
are the ones the spec leaves open; the default is the recommended one and is what the flip should do
unless the owner says otherwise.

| Field (HEAD `OperationDef`) | Decision | Reason and native home | Decided by |
|---|---|---|---|
| `FormerIDs` (author-split-scan, resolve-production-authors, purge-deleted, tombstone-cleanup, temp-file-cleanup, cleanup-old-backups, trash-cleanup, db-optimize, metadata-refresh, library.optimize, itunes-heal, itunes-regroup, itunes-clone-into-library, itunes-playlist-import) | **Move** to the kind-rename map | A native kind has no alias field. Every HEAD canonical id and every `FormerIDs` alias maps to one registered kind in the rename map generated at the flip (`internal/books/cutover/kindrename`, F5-18 commit 2 area; `05-tasks.md:1952-1959`). `internal/server/testdata/op_ids.golden` already lists every alias, and `TestKindRenameMapCoversLedger` fails if one is missing. Kinds with no imported runs need none (the F5-03 and F5-04 notes). | Spec, `05-tasks.md` section on the rename map |
| `Liveness` (`LivenessManual`, `LivenessRunItems`, `LivenessNone`) | **Drop** | The kernel's watchdog replaces it: a `RunItems` loop touches liveness on every completed item; a non-items body reports progress within `ProgressTimeout` (default `Timeout/6`, min 1 min) or declares `Uninterruptible` (`05-tasks.md:433`, `:634`). Native `Common.ProgressTimeout` carries a kind's own value where HEAD set one. | Spec |
| `Cancellable` | **Drop** | The field was persisted but never read: `Registry.Cancel` ignores it (`05-tasks.md:79`, `:2297`; `raw/06-tasks-ops.md:44`). Every native run is cancellable through its context. The HEAD `Cancellable:false` defs (db-optimize, purge-deleted, tombstone-cleanup, temp-file-cleanup, cleanup-old-backups, trash-cleanup and others) were never actually protected from cancel, and db-optimize's compaction ignored it besides (`db.go` comment). | Spec |
| `Plugin` (`"maintenance"`) | **Drop** | The owner module is `books`: the kind name carries it (`books.<id>`), and registration names the module. A plugin tag has no meaning in the native engine. | Spec (kind naming, `R5`) |
| `0 6 * * *` metadata refresh cron | **Drop the string; the cadence moves to F5-14's declaration** | The 15 HEAD cron strings were never evaluated; D15 deletes all 15. 14 of the 15 defs are fired by one of the 38 scheduler `TaskDefinition`s and keep that definition's cadence through `books.ScheduleFor`. `TestScheduleFor_DroppedV2CronsAbsent` fails if a string comes back. | D15, `F5-14-schedules-oracle-no-catchup-storm.md` |
| `10 0 * * *` nightly activity-log compaction | **Same as above** | Same 15-string list (`nightly_compact_activity_log.go`); the native kind takes `sched(kind)`, which F5-18 passes as `books.ScheduleFor`. | D15, F5-14 |
| `15 2 * * *` orphan book-file scan | **Same as above** | Same 15-string list (`orphan_book_files.go`). | D15, F5-14 |
| `scheduler.*` FormerIDs for kinds that were scheduler-only (author-split-scan, resolve-production-authors) | **Move** with `FormerIDs` | The scheduler copy and the plugin copy were merged into one native kind (`author.go` comment); the alias goes in the rename map, and the kind's schedule through `ScheduleFor`. | Spec |
| Preview/dry-run param specs, `Capabilities`, `Isolate` | **Drop** | The run's mode (preview or live) is the start request's; a native run refuses `dry_run`, `dryRun` and `apply` (`t6g1ModeKeys`). Capabilities and isolation are not native concepts (F5-01, F5-05 notes). Native Help already says so (F5-18f, #4968). | Spec |

Open items: none. The one place a default was needed is a behaviour note, not a field: a HEAD
`Cancellable:false` job becomes cancellable natively. That follows from the spec (the HEAD flag never
blocked a cancel), so it is recorded here, not decided here, and no kind sets `Uninterruptible` for it
(default, owner asleep: do not set it; a kind that reports no progress for longer than its
`ProgressTimeout` should set a `ProgressTimeout` of its own instead).
