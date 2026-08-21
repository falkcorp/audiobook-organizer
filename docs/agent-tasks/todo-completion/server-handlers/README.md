<!-- file: docs/agent-tasks/todo-completion/server-handlers/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 14224fda-5ee2-4c67-91dc-8190eecdd7e2 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — server-handlers (todo-completion)

14 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-152 | MERGE-UNDO | Expose UnmergeAuto through an admin undo-merge endpoint (list + invoke | P1 | M | Opus-class | 1 |
| TASK-153 | ABS-N3 | N-3: stop advertising Delete/Update permissions the library surface ca | P2 | S | Sonnet-class | 1 |
| TASK-154 | ABS-N5 | N-5: /search narrators must omit numBooks, not emit 0 | P2 | S | Haiku-class | 4 |
| TASK-155 | ABS-N6 | N-6: log + metric when listening-stats read fails (currently silent 0) | P2 | S | Haiku-class | 1 |
| TASK-156 | ABS-N10 | N-10: advertised login rate limit (10/10min) does not match the real t | P2 | S | Haiku-class | 2 |
| TASK-157 | L127 | Align ABS conformance fixtures with the oracle so CompareValues stays  | P2 | L | Opus-class | 5 |
| TASK-158 | L685 | Detect multi-file books whose synthesized chapter timeline stops short | P2 | S | Sonnet-class | 1 |
| TASK-159 | L2589 | Document the hardcoded ABS timeBase as a permanent, owner-approved all | P2 | S | Haiku-class | 2 |
| TASK-160 | PERF-4 | Bound the iTunes search handler's unbounded SearchBooks(search, 0, 0)  | P2 | S | Sonnet-class | 1 |
| TASK-161 | L4507 | Implement POST /api/session/local (2xx stub) | P2 | S | Haiku-class | 1 |
| TASK-162 | L4507 | Implement POST /api/session/local-all (batch local-session sync, accep | P2 | M | Sonnet-class | 2 |
| TASK-163 | L4563 | Move /tasks/* and /maintenance-window/* off the legacy v1 operations h | P2 | M | Sonnet-class | 1 |
| TASK-164 | ABS-SYNC-Phase7 | Phase 7 — socket.io for Absorb (deprioritized by the item's own text;  | P2 | M | Sonnet-class | 2 |
| TASK-165 | L10521 | Parallelize the per-candidate synchronous label/breakdown refresh in D | P1 | M | Opus-class | 2 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  make ci ; make ci && npm --prefix web run lint && npm --prefix web test
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/server/handlers/abs/browse.go`: TASK-087, TASK-088, TASK-098, TASK-154 → serialize by wave (TASK-087=w1, TASK-088=w2, TASK-098=w3, TASK-154=w4)
- `internal/server/handlers/abs/dto.go`: TASK-153, TASK-156 → serialize by wave (TASK-153=w1, TASK-156=w2)
- `internal/server/handlers/abs/handler.go`: TASK-161, TASK-162 → serialize by wave (TASK-161=w1, TASK-162=w2)
- `internal/server/handlers/abs/library_fake_test.go`: TASK-087, TASK-088, TASK-157 → serialize by wave (TASK-087=w1, TASK-088=w2, TASK-157=w5)
- `internal/server/handlers/abs/mapper.go`: TASK-158, TASK-159 → serialize by wave (TASK-158=w1, TASK-159=w2)
- `internal/server/handlers/abs/play.go`: TASK-161, TASK-162 → serialize by wave (TASK-161=w1, TASK-162=w2)
- `internal/server/handlers/dedup/handler.go`: TASK-152, TASK-165 → serialize by wave (TASK-152=w1, TASK-165=w2)
- `internal/server/wire_abs_routes.go`: TASK-136, TASK-164 → serialize by wave (TASK-136=w1, TASK-164=w2)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-152, TASK-153, TASK-155, TASK-158, TASK-160, TASK-161, TASK-163 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-156, TASK-159, TASK-162, TASK-164, TASK-165 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 4 | TASK-154 | wave 3 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 5 | TASK-157 | wave 4 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
