<!-- file: docs/agent-tasks/todo-completion/search/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: f81c3b20-42b5-4868-a028-18a32dd245ff -->
<!-- last-edited: 2026-08-21 -->

# Workstream — search (todo-completion)

2 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-129 | L618 | Index track names on BookDocument so smart playlists can match them | P2 | M | Sonnet-class | 1 |
| TASK-130 | L3369 | Surface to the user when 'all'/'and' (or any stopword) is silently dro | P2 | M | Sonnet-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  make ci
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/search/bleve_index.go`: TASK-023, TASK-129 → serialize by wave (TASK-023=w2, TASK-129=w1)
- `internal/search/index_builder.go`: TASK-023, TASK-129 → serialize by wave (TASK-023=w2, TASK-129=w1)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-129, TASK-130 | none | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
