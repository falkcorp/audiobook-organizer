<!-- file: docs/agent-tasks/todo-completion/scanner/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: b0b5f49d-8cfb-40ba-8d37-30ce07e2b6e3 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — scanner (todo-completion)

2 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-127 | L4739 | Delete the unused internal/scanner/mocks generated package | P2 | S | Haiku-class | 2 |
| TASK-128 | L4852 | Reuse internal/ai's existing typed OpenAI error classification in scan | P2 | M | Sonnet-class | 1 |

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

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-128 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-127 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
