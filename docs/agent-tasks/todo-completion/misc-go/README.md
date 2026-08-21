<!-- file: docs/agent-tasks/todo-completion/misc-go/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: be743986-9855-4d4b-b260-fd767e87415f -->
<!-- last-edited: 2026-08-21 -->

# Workstream — misc-go (todo-completion)

8 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-090 | VG-DOUBLE-PRIMARY | Measure the real double-primary rate library-wide, then build the demo | P1 | M | Opus-class | 1 |
| TASK-091 | SEC-CODEQL-BACKLOG | Fix the go/zipslip finding on the backup-restore extraction path | P1 | M | Opus-class | 1 |
| TASK-092 | SEC-CODEQL-BACKLOG | Fix or verify the 4 still-open go/path-injection findings (1 of the or | P1 | M | Opus-class | 1 |
| TASK-093 | SEC-CODEQL-BACKLOG | Add CodeQL-specific lgtm suppressions for the 3 already-justified go/d | P2 | S | Sonnet-class | 1 |
| TASK-094 | L3433 | Add search-index metrics (docs total, dirty backlog) to /metrics — the | P2 | M | Sonnet-class | 1 |
| TASK-095 | L3790 | Collapse internal whitespace in util.NormalizeAuthor so double-spaced  | P1 | S | Sonnet-class | 1 |
| TASK-096 | ARCH-8 | Replace serviceregistry.Get[T]'s panicking string-key lookups with typ | P2 | M | Sonnet-class | 1 |
| TASK-097 | L4698 | Route acoustid lsh_backfill's lshIndexChecker lookup through database. | P2 | S | Haiku-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  make ci ; make ci && npm --prefix web run lint && npm --prefix web test
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/metrics/metrics.go`: TASK-094, TASK-140, TASK-141 → serialize by wave (TASK-094=w1, TASK-140=w2, TASK-141=w4)
- `web/package-lock.json`: TASK-096, TASK-101 → serialize by wave (TASK-096=w1, TASK-101=w2)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-090, TASK-091, TASK-092, TASK-093, TASK-094, TASK-095, TASK-096, TASK-097 | none | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
