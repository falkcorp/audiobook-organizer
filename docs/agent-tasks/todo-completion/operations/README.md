<!-- file: docs/agent-tasks/todo-completion/operations/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 50fc600d-4f1a-4215-9347-2226d651ffc4 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — operations (todo-completion)

4 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-119 | L4477 | Distinguish 'nothing to cancel' from 'cancelled' in registry.Cancel so | P2 | M | Sonnet-class | 1 |
| TASK-120 | L4586 | Forward IsCanceled() through reporterLogger to the ops registry's canc | P1 | M | Opus-class | 1 |
| TASK-121 | L4703 | Give prodSchedulerStore an Unwrap() so capability lookups can see past | P2 | S | Sonnet-class | 1 |
| TASK-122 | L4743 | Delete internal/operations/mocks — its only referencer is dead, perman | P2 | S | Sonnet-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  make ci
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `.mockery.yaml`: TASK-122, TASK-127 → serialize by wave (TASK-122=w1, TASK-127=w2)
- `internal/operations/registry/registry.go`: TASK-100, TASK-119 → serialize by wave (TASK-100=w2, TASK-119=w1)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-119, TASK-120, TASK-121, TASK-122 | none | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
