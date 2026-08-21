<!-- file: docs/agent-tasks/todo-completion/maintenance/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0ca45232-c7a5-4762-886e-8f3a1b20bd55 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — maintenance (todo-completion)

13 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-074 | L606 | Wire a durable freshness stamp for maintenance.chapters-backfill befor | P2 | M | Sonnet-class | 2 |
| TASK-075 | L642 | Extend the REPOINT repair to recover BookFile rows via Book.FilePath ( | P1 | M | Opus-class | 2 |
| TASK-076 | L670 | Build a REPORT-ONLY counter for Book.FilePath collisions (rows sharing | P2 | S | Sonnet-class | 1 |
| TASK-077 | L1009 | Give maintenance jobs (v1, internal/maintenance) per-job store interfa | P2 | L | Sonnet-class | 1 |
| TASK-078 | L3488 | Add a user-configurable activity-log retention window (default 7 days, | P2 | M | Sonnet-class | 6 |
| TASK-079 | L3602 | Build a detection-only report of other title-fragment author rows (the | P2 | M | Sonnet-class | 1 |
| TASK-080 | L3795 | New maintenance op: merge an operator-confirmed list of duplicate real | P1 | M | Opus-class | 2 |
| TASK-081 | L4137 | Read-through audit of the 8 ctxOpID consumer call sites now that op ID | P1 | M | Opus-class | 1 |
| TASK-082 | L4144 | Build a report-only census of books with a placeholder author already  | P2 | M | Sonnet-class | 1 |
| TASK-083 | L5275 | Extend purge-empty-authors' report to categorize the 822 zero-book-but | P2 | S | Sonnet-class | 1 |
| TASK-084 | L5281 | Author-narrator swap repair, routed through the review queue (cross-ta | P1 | L | Opus-class | 1 |
| TASK-085 | L5424 | Narrow the 3 remaining maintenance-jobs callees off maintenance.JobSto | P2 | M | Sonnet-class | 1 |
| TASK-086 | ABS-SYNC-TASK-04 | TASK-04: build the idempotent sync-ID backfill over the existing libra | P1 | M | Opus-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  make ci ; make ci && npm --prefix web run lint && npm --prefix web test
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `changelog.d/20260821_metadata_081.md`: TASK-079, TASK-080 → serialize by wave (TASK-079=w1, TASK-080=w2)
- `internal/config/config.go`: TASK-017, TASK-018, TASK-019, TASK-020, TASK-021, TASK-078 → serialize by wave (TASK-017=w1, TASK-018=w2, TASK-019=w3, TASK-020=w4, TASK-021=w5, TASK-078=w6)
- `internal/metafetch/service_apply.go`: TASK-079, TASK-080, TASK-089, TASK-129 → serialize by wave (TASK-079=w1, TASK-080=w2, TASK-089=w3, TASK-129=w4)
- `internal/metafetch/service_apply_test.go`: TASK-079, TASK-080 → serialize by wave (TASK-079=w1, TASK-080=w2)
- `internal/plugins/maintenance/cleanup.go`: TASK-078, TASK-081 → serialize by wave (TASK-078=w6, TASK-081=w1)
- `internal/plugins/maintenance/deps.go`: TASK-024, TASK-074, TASK-078 → serialize by wave (TASK-024=w1, TASK-074=w2, TASK-078=w6)
- `internal/server/server_maintenance_deps.go`: TASK-024, TASK-074, TASK-078 → serialize by wave (TASK-024=w1, TASK-074=w2, TASK-078=w6)
- `web/src/pages/ActivityLog.tsx`: TASK-078, TASK-184 → serialize by wave (TASK-078=w6, TASK-184=w1)
- `web/src/services/api.ts`: TASK-039, TASK-078 → serialize by wave (TASK-039=w3, TASK-078=w6)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-076, TASK-077, TASK-079, TASK-081, TASK-082, TASK-083, TASK-084, TASK-085, TASK-086 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-074, TASK-075, TASK-080 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 6 | TASK-078 | wave 5 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
