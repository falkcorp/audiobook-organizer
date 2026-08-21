<!-- file: docs/agent-tasks/todo-completion/maintenance/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 22cc6aac-ce3b-43d0-b787-344c773e0939 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — maintenance (todo-completion)

13 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-069 | L606 | Wire a durable freshness stamp for maintenance.chapters-backfill befor | P2 | M | Sonnet-class | 1 |
| TASK-070 | L642 | Extend the REPOINT repair to recover BookFile rows via Book.FilePath ( | P1 | M | Opus-class | 2 |
| TASK-071 | L670 | Build a REPORT-ONLY counter for Book.FilePath collisions (rows sharing | P2 | S | Sonnet-class | 1 |
| TASK-072 | L1009 | Give maintenance jobs (v1, internal/maintenance) per-job store interfa | P2 | L | Sonnet-class | 1 |
| TASK-073 | L3488 | Add a user-configurable activity-log retention window (default 7 days, | P2 | M | Sonnet-class | 5 |
| TASK-074 | L3602 | Build a detection-only report of other title-fragment author rows (the | P2 | M | Sonnet-class | 1 |
| TASK-075 | L3795 | New maintenance op: merge an operator-confirmed list of duplicate real | P1 | M | Opus-class | 3 |
| TASK-076 | L4137 | Read-through audit of the 8 ctxOpID consumer call sites now that op ID | P1 | M | Opus-class | 1 |
| TASK-077 | L4144 | Build a report-only census of books with a placeholder author already  | P2 | M | Sonnet-class | 1 |
| TASK-078 | L5275 | Extend purge-empty-authors' report to categorize the 822 zero-book-but | P2 | S | Sonnet-class | 1 |
| TASK-079 | L5281 | Author-narrator swap repair, routed through the review queue (cross-ta | P1 | L | Opus-class | 1 |
| TASK-080 | L5424 | Narrow the 3 remaining maintenance-jobs callees off maintenance.JobSto | P2 | M | Sonnet-class | 1 |
| TASK-081 | ABS-SYNC-TASK-04 | TASK-04: build the idempotent sync-ID backfill over the existing libra | P1 | M | Opus-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  make ci ; make ci && npm --prefix web run lint && npm --prefix web test
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `changelog.d/20260821_metadata_081.md`: TASK-074, TASK-075 → serialize by wave (TASK-074=w1, TASK-075=w3)
- `internal/config/config.go`: TASK-015, TASK-016, TASK-017, TASK-018, TASK-019, TASK-073 → serialize by wave (TASK-015=w6, TASK-016=w1, TASK-017=w2, TASK-018=w3, TASK-019=w4, TASK-073=w5)
- `internal/metafetch/service_apply.go`: TASK-074, TASK-075, TASK-084, TASK-124 → serialize by wave (TASK-074=w1, TASK-075=w3, TASK-084=w2, TASK-124=w4)
- `internal/metafetch/service_apply_test.go`: TASK-074, TASK-075 → serialize by wave (TASK-074=w1, TASK-075=w3)
- `internal/plugins/maintenance/cleanup.go`: TASK-073, TASK-076 → serialize by wave (TASK-073=w5, TASK-076=w1)
- `internal/plugins/maintenance/deps.go`: TASK-022, TASK-069, TASK-073 → serialize by wave (TASK-022=w2, TASK-069=w1, TASK-073=w5)
- `internal/server/server_maintenance_deps.go`: TASK-022, TASK-069, TASK-073 → serialize by wave (TASK-022=w2, TASK-069=w1, TASK-073=w5)
- `web/src/pages/ActivityLog.tsx`: TASK-073, TASK-179 → serialize by wave (TASK-073=w5, TASK-179=w1)
- `web/src/services/api.ts`: TASK-037, TASK-073 → serialize by wave (TASK-037=w6, TASK-073=w5)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-069, TASK-071, TASK-072, TASK-074, TASK-076, TASK-077, TASK-078, TASK-079, TASK-080, TASK-081 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-070 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 3 | TASK-075 | wave 2 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 5 | TASK-073 | wave 4 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
