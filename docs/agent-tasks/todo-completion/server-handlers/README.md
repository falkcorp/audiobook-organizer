<!-- file: docs/agent-tasks/todo-completion/server-handlers/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9686d0e4-dd7d-4521-87b4-803a08d79602 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — server-handlers (todo-completion)

19 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-142 | MERGE-UNDO | Expose UnmergeAuto through an admin undo-merge endpoint (list + invoke | P1 | M | Opus-class | 1 |
| TASK-143 | ABS-N3 | N-3: stop advertising Delete/Update permissions the library surface ca | P2 | S | Sonnet-class | 1 |
| TASK-144 | ABS-N5 | N-5: /search narrators must omit numBooks, not emit 0 | P2 | S | Haiku-class | 2 |
| TASK-145 | ABS-N6 | N-6: log + metric when listening-stats read fails (currently silent 0) | P2 | S | Haiku-class | 1 |
| TASK-146 | ABS-N10 | N-10: advertised login rate limit (10/10min) does not match the real t | P2 | S | Haiku-class | 3 |
| TASK-147 | L127 | Align ABS conformance fixtures with the oracle so CompareValues stays  | P2 | L | Opus-class | 4 |
| TASK-212 | L476 | Add GET /api/libraries/:libraryId/series/:seriesId to the ABS surface | P2 | M | Sonnet-class | 3 |
| TASK-148 | L491 | Re-capture the series ABS fixture against a populated library (it curr | P2 | S | Sonnet-class | 5 |
| TASK-149 | L685 | Detect multi-file books whose synthesized chapter timeline stops short | P2 | S | Sonnet-class | 1 |
| TASK-213 | ORGANIZE-4TH-COPY | Replace the single-file OrganizeBook call in filesystem.go's auto-orga | P1 | S | Opus-class | 2 |
| TASK-150 | L2481 | Audit apply-shaped endpoints for missing tag/file-I/O writeback | P1 | M | Opus-class | 2 |
| TASK-151 | L2589 | Document the hardcoded ABS timeBase as a permanent, owner-approved all | P2 | S | Haiku-class | 2 |
| TASK-152 | PERF-4 | Bound the iTunes search handler's unbounded SearchBooks(search, 0, 0)  | P2 | S | Sonnet-class | 1 |
| TASK-153 | L4507 | Implement POST /api/session/local (2xx stub) | P2 | S | Haiku-class | 1 |
| TASK-154 | L4507 | Implement POST /api/session/local-all (batch local-session sync, accep | P2 | M | Sonnet-class | 2 |
| TASK-155 | L4563 | Move /tasks/* and /maintenance-window/* off the legacy v1 operations h | P2 | M | Sonnet-class | 1 |
| TASK-156 | ABS-SYNC-Phase7 | Phase 7 — socket.io for Absorb (deprioritized by the item's own text;  | P2 | M | Sonnet-class | 2 |
| TASK-157 | L10521 | Parallelize the per-candidate synchronous label/breakdown refresh in D | P1 | M | Opus-class | 2 |
| TASK-214 | REV-EMPTY-2 | Cap GET /api/v1/audiobooks/metadata/cache/review to a default page siz | P2 | L | Sonnet-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only' ; go build ./... && go vet ./... && go test ./internal/server/... ./internal/server/handlers/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/... ./internal/server/handlers/... ./internal/server/handlers/operations/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/... ./internal/server/handlers/abs/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/... ./internal/server/handlers/dedup/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/handlers/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/handlers/... ./internal/server/handlers/metadata/... ./internal/server/handlers/review/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/handlers/... ./internal/server/handlers/mocks/... -count=1 && npm --prefix web run lint && npm --prefix web test ; go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/handlers/dedup/... -count=1
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/server/handlers/abs/abs_test.go`: TASK-143, TASK-144, TASK-146 → serialize by wave (TASK-143=w1, TASK-144=w2, TASK-146=w3)
- `internal/server/handlers/abs/browse.go`: TASK-089, TASK-144, TASK-212 → serialize by wave (TASK-089=w1, TASK-144=w2, TASK-212=w3)
- `internal/server/handlers/abs/dto.go`: TASK-143, TASK-146 → serialize by wave (TASK-143=w1, TASK-146=w3)
- `internal/server/handlers/abs/handler.go`: TASK-212, TASK-153, TASK-154 → serialize by wave (TASK-212=w3, TASK-153=w1, TASK-154=w2)
- `internal/server/handlers/abs/library_fake_test.go`: TASK-089, TASK-147 → serialize by wave (TASK-089=w1, TASK-147=w4)
- `internal/server/handlers/abs/mapper.go`: TASK-149, TASK-151 → serialize by wave (TASK-149=w1, TASK-151=w2)
- `internal/server/handlers/abs/play.go`: TASK-153, TASK-154 → serialize by wave (TASK-153=w1, TASK-154=w2)
- `internal/server/handlers/abs/play_test.go`: TASK-153, TASK-154 → serialize by wave (TASK-153=w1, TASK-154=w2)
- `internal/server/handlers/dedup/handler.go`: TASK-142, TASK-157 → serialize by wave (TASK-142=w1, TASK-157=w2)
- `internal/server/handlers/dedup/handler_test.go`: TASK-142, TASK-157 → serialize by wave (TASK-142=w1, TASK-157=w2)
- `internal/server/handlers/filesystem.go`: TASK-083, TASK-213 → serialize by wave (TASK-083=w1, TASK-213=w2)
- `internal/server/handlers/metadata_cache.go`: TASK-150, TASK-214 → serialize by wave (TASK-150=w2, TASK-214=w1)
- `internal/server/handlers/metadata_cache_test.go`: TASK-150, TASK-214 → serialize by wave (TASK-150=w2, TASK-214=w1)
- `internal/server/wire_abs_routes.go`: TASK-127, TASK-212, TASK-156 → serialize by wave (TASK-127=w1, TASK-212=w3, TASK-156=w2)
- `web/src/components/review/lanes/useMetadataLane.ts`: TASK-214, TASK-165, TASK-217 → serialize by wave (TASK-214=w1, TASK-165=w7, TASK-217=w2)
- `web/src/services/api.ts`: TASK-037, TASK-070, TASK-214 → serialize by wave (TASK-037=w5, TASK-070=w6, TASK-214=w1)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-142, TASK-143, TASK-145, TASK-149, TASK-152, TASK-153, TASK-155, TASK-214 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-144, TASK-213, TASK-150, TASK-151, TASK-154, TASK-156, TASK-157 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 3 | TASK-146, TASK-212 | wave 2 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 4 | TASK-147 | wave 3 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 5 | TASK-148 | wave 4 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
