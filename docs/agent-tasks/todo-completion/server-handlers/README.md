<!-- file: docs/agent-tasks/todo-completion/server-handlers/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: a3d2e2af-2a78-4c1c-a41a-b8bce6c7b9b4 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — server-handlers (todo-completion)

14 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-147 | MERGE-UNDO | Expose UnmergeAuto through an admin undo-merge endpoint (list + invoke | P1 | M | Opus-class | 1 |
| TASK-148 | ABS-N3 | N-3: stop advertising Delete/Update permissions the library surface ca | P2 | S | Sonnet-class | 1 |
| TASK-149 | ABS-N5 | N-5: /search narrators must omit numBooks, not emit 0 | P2 | S | Haiku-class | 3 |
| TASK-150 | ABS-N6 | N-6: log + metric when listening-stats read fails (currently silent 0) | P2 | S | Haiku-class | 1 |
| TASK-151 | ABS-N10 | N-10: advertised login rate limit (10/10min) does not match the real t | P2 | S | Haiku-class | 2 |
| TASK-152 | L127 | Align ABS conformance fixtures with the oracle so CompareValues stays  | P2 | L | Opus-class | 4 |
| TASK-153 | L685 | Detect multi-file books whose synthesized chapter timeline stops short | P2 | S | Sonnet-class | 1 |
| TASK-154 | L2589 | Document the hardcoded ABS timeBase as a permanent, owner-approved all | P2 | S | Haiku-class | 2 |
| TASK-155 | PERF-4 | Bound the iTunes search handler's unbounded SearchBooks(search, 0, 0)  | P2 | S | Sonnet-class | 1 |
| TASK-156 | L4507 | Implement POST /api/session/local (2xx stub) | P2 | S | Haiku-class | 1 |
| TASK-157 | L4507 | Implement POST /api/session/local-all (batch local-session sync, accep | P2 | M | Sonnet-class | 2 |
| TASK-158 | L4563 | Move /tasks/* and /maintenance-window/* off the legacy v1 operations h | P2 | M | Sonnet-class | 1 |
| TASK-159 | ABS-SYNC-Phase7 | Phase 7 — socket.io for Absorb (deprioritized by the item's own text;  | P2 | M | Sonnet-class | 2 |
| TASK-160 | L10521 | Parallelize the per-candidate synchronous label/breakdown refresh in D | P1 | M | Opus-class | 2 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  make ci ; make ci && npm --prefix web run lint && npm --prefix web test
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/server/handlers/abs/browse.go`: TASK-082, TASK-083, TASK-093, TASK-149 → serialize by wave (TASK-082=w1, TASK-083=w5, TASK-093=w2, TASK-149=w3)
- `internal/server/handlers/abs/dto.go`: TASK-148, TASK-151 → serialize by wave (TASK-148=w1, TASK-151=w2)
- `internal/server/handlers/abs/handler.go`: TASK-156, TASK-157 → serialize by wave (TASK-156=w1, TASK-157=w2)
- `internal/server/handlers/abs/library_fake_test.go`: TASK-082, TASK-083, TASK-152 → serialize by wave (TASK-082=w1, TASK-083=w5, TASK-152=w4)
- `internal/server/handlers/abs/mapper.go`: TASK-153, TASK-154 → serialize by wave (TASK-153=w1, TASK-154=w2)
- `internal/server/handlers/abs/play.go`: TASK-156, TASK-157 → serialize by wave (TASK-156=w1, TASK-157=w2)
- `internal/server/handlers/dedup/handler.go`: TASK-147, TASK-160 → serialize by wave (TASK-147=w1, TASK-160=w2)
- `internal/server/wire_abs_routes.go`: TASK-131, TASK-159 → serialize by wave (TASK-131=w1, TASK-159=w2)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-147, TASK-148, TASK-150, TASK-153, TASK-155, TASK-156, TASK-158 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-151, TASK-154, TASK-157, TASK-159, TASK-160 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 3 | TASK-149 | wave 2 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 4 | TASK-152 | wave 3 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
