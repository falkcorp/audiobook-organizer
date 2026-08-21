<!-- file: docs/agent-tasks/todo-completion/organize/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 477a0c67-d186-471b-befa-916cf1a22383 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — organize (todo-completion)

4 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-123 | F5 | Replace the size-equality heuristic in OrganizeBookDirectory's destina | P1 | S | Sonnet-class | 1 |
| TASK-124 | F5 | Route the three organize/rename paths through organizer.MoveBookFile's | P1 | L | Opus-class | 4 |
| TASK-125 | L4919 | Make resolveOrganizedFilePath's plan-on-faith fallback loud and verify | P1 | M | Opus-class | 1 |
| TASK-126 | L5021 | Add an {edition_suffix} folder-pattern token | P1 | S | Sonnet-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  make ci
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/metafetch/service_apply.go`: TASK-074, TASK-075, TASK-084, TASK-124 → serialize by wave (TASK-074=w1, TASK-075=w3, TASK-084=w2, TASK-124=w4)
- `internal/organizer/organizer.go`: TASK-123, TASK-124 → serialize by wave (TASK-123=w1, TASK-124=w4)
- `internal/organizer/service.go`: TASK-085, TASK-125 → serialize by wave (TASK-085=w4, TASK-125=w1)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-123, TASK-125, TASK-126 | none | disjoint files within the wave (computed collision matrix) |
| 4 | TASK-124 | wave 3 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
