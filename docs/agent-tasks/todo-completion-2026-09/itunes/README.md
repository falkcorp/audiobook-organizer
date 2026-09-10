<!-- file: docs/agent-tasks/todo-completion-2026-09/itunes/README.md -->
<!-- version: 1.7.0 -->
<!-- guid: c25e2843-84d7-45ac-9a69-e0a37b612387 -->
<!-- last-edited: 2026-09-10 -->

# Workstream — itunes (todo-completion-2026-09)

9 tasks: 6 carried forward from the 2026-08-21 package (ids kept), 3 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-062](TASK-062-internal-itunes-backfill-go-backfillexternalids-.md) | carried | perf | P2 | M | internal/itunes/backfill.go BackfillExternalIDs: replace offset pagination with  | internal/itunes/backfill.go:60 still 'offset := 0'. grep GetAllBooksFullFrom backfill.go = |
| [TASK-063](TASK-063-internal-itunes-backfill-go-backfillitunestrackp.md) | carried | perf | P2 | S | internal/itunes/backfill.go BackfillITunesTrackPIDs: same offset-pagination bug | internal/itunes/backfill.go:178 still 'offset := 0' (2 total occurrences in the file, L60  |
| [TASK-064](TASK-064-add-a-part-disc-chapter-track-filename-parser-so.md) | carried | correctness | P2 | M | Add a Part->disc / Chapter->track filename parser so 'P0-C0'-style folders stop  | The file has moved to internal/itunes/service/fs_regroup_shape.go (path drifted from inter |
| [TASK-065](TASK-065-p2-relocate-only-sync-cycle-the-composed-cycle-a.md) | carried | correctness | P2 | M | P2 relocate-only sync cycle -- wire RunRelocateSyncCycle to a caller and add an  | grep -rln RunRelocateSyncCycle internal/ = 1 file, internal/itunes/relocate_sync_cycle.go  |
| [TASK-184](TASK-184-measure-itunes-xml-track-persistent-id-coverage-.md) | carried | hygiene | P2 | S | Measure iTunes XML track Persistent ID coverage against the local DB before prom | find . -iname 'pid_coverage*' = 0 results at HEAD. |
| [TASK-185](TASK-185-report-the-itunes-listened-in-progress-status-pi.md) | carried | hygiene | P2 | S | Report the iTunes listened/in-progress status pipeline's actual wiring gap | docs/audits/2026-08-21-itunes-playback-import-wiring.md still absent; docs/audits/ jumps f |
| [TASK-323](TASK-323-external-id-backfill-s-done-setting-is-written-b.md) | new-finding | perf | P1 | S | External-ID backfill's "done" setting is written but never read -- the full-libr | internal/itunes/backfill.go:56 |
| [TASK-340](TASK-340-internal-itunes-service-writeback-batcher-go-sto.md) | new-todo | data-loss | P1 | M | `internal/itunes/service/writeback_batcher.go` — `Stop()` (`:814`) sets a flag a | TODO.md lines 1461 |
| [TASK-375](TASK-375-itunes-2-way-sync-writeback-edit-in-place-preser.md) | new-todo | data-loss | P1 | L | iTunes 2-way sync writeback (edit-in-place, preserve play-state) | TODO.md lines 17329 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
