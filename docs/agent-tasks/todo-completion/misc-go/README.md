<!-- file: docs/agent-tasks/todo-completion/misc-go/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: e3aa44d5-7a16-4c2f-b058-9dc080ec34c6 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — misc-go (todo-completion)

8 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-085 | VG-DOUBLE-PRIMARY | Measure the real double-primary rate library-wide, then build the demo | P1 | M | Opus-class | 4 |
| TASK-086 | SEC-CODEQL-BACKLOG | Fix the go/zipslip finding on the backup-restore extraction path | P1 | M | Opus-class | 1 |
| TASK-087 | SEC-CODEQL-BACKLOG | Fix or verify the 4 still-open go/path-injection findings (1 of the or | P1 | M | Opus-class | 1 |
| TASK-088 | SEC-CODEQL-BACKLOG | Add CodeQL-specific lgtm suppressions for the 3 already-justified go/d | P2 | S | Sonnet-class | 1 |
| TASK-089 | L3433 | Add search-index metrics (docs total, dirty backlog) to /metrics — the | P2 | M | Sonnet-class | 1 |
| TASK-090 | L3790 | Collapse internal whitespace in util.NormalizeAuthor so double-spaced  | P1 | S | Sonnet-class | 1 |
| TASK-091 | ARCH-8 | Replace serviceregistry.Get[T]'s panicking string-key lookups with typ | P2 | M | Sonnet-class | 1 |
| TASK-092 | L4698 | Route acoustid lsh_backfill's lshIndexChecker lookup through database. | P2 | S | Haiku-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  make ci ; make ci && npm --prefix web run lint && npm --prefix web test
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/audiobooks/service_filtering.go`: TASK-004, TASK-085 → serialize by wave (TASK-004=w1, TASK-085=w4)
- `internal/audiobooks/service_query.go`: TASK-001, TASK-004, TASK-085 → serialize by wave (TASK-001=w3, TASK-004=w1, TASK-085=w4)
- `internal/database/dbtest/invariants.go`: TASK-022, TASK-085 → serialize by wave (TASK-022=w2, TASK-085=w4)
- `internal/database/pebble_store.go`: TASK-029, TASK-039, TASK-085 → serialize by wave (TASK-029=w2, TASK-039=w3, TASK-085=w4)
- `internal/metrics/metrics.go`: TASK-089, TASK-135, TASK-136 → serialize by wave (TASK-089=w1, TASK-135=w2, TASK-136=w3)
- `internal/organizer/service.go`: TASK-085, TASK-125 → serialize by wave (TASK-085=w4, TASK-125=w1)
- `web/package-lock.json`: TASK-091, TASK-096 → serialize by wave (TASK-091=w1, TASK-096=w2)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-086, TASK-087, TASK-088, TASK-089, TASK-090, TASK-091, TASK-092 | none | disjoint files within the wave (computed collision matrix) |
| 4 | TASK-085 | wave 3 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
