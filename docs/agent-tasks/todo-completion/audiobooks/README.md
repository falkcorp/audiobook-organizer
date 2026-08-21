<!-- file: docs/agent-tasks/todo-completion/audiobooks/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 33d6ad6b-4106-496f-b367-ba7669f863cd -->
<!-- last-edited: 2026-08-21 -->

# Workstream — audiobooks (todo-completion)

6 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-001 | SEARCH-CACHE | Add a short-TTL cache to the search branch of GetAudiobooksWithTotal ( | P1 | M | Opus-class | 2 |
| TASK-002 | L3348 | Fix the 3-way disagreement in how a nil IsPrimaryVersion is treated (m | P1 | M | Opus-class | 1 |
| TASK-003 | L3354 | Build a read-only census tool for the 41 ungrouped-but-explicitly-non- | P2 | S | Sonnet-class | 1 |
| TASK-004 | L3884 | Fix the author-path post-filter to treat nil IsPrimaryVersion as prima | P1 | S | Sonnet-class | 3 |
| TASK-005 | L3889 | Add a conformance test asserting the library path and author path clas | P1 | S | Sonnet-class | 4 |
| TASK-006 | L10728 | Wire OnlyParsedTranscription-style filtering into the interactive audi | P2 | M | Sonnet-class | 4 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  make ci
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/audiobooks/service_filtering.go`: TASK-002, TASK-006 → serialize by wave (TASK-002=w1, TASK-006=w4)
- `internal/audiobooks/service_query.go`: TASK-001, TASK-002, TASK-004, TASK-006 → serialize by wave (TASK-001=w2, TASK-002=w1, TASK-004=w3, TASK-006=w4)
- `internal/server/handlers/audiobooks/handler.go`: TASK-006, TASK-039, TASK-104, TASK-107 → serialize by wave (TASK-006=w4, TASK-039=w3, TASK-104=w1, TASK-107=w2)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-002, TASK-003 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-001 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 3 | TASK-004 | wave 2 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 4 | TASK-005, TASK-006 | wave 3 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
