<!-- file: docs/agent-tasks/todo-completion/server/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: da52e44e-fce4-438d-a16a-1517d0e12cdf -->
<!-- last-edited: 2026-08-21 -->

# Workstream — server (todo-completion)

22 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-127 | N-11 | Log ABS_API_ENABLED's actual boot-time value unconditionally (currentl | P2 | S | Haiku-class | 1 |
| TASK-204 | L280 | Guard TestServerStartGracefulShutdown's SIGTERM against future paralle | P2 | S | Haiku-class | 1 |
| TASK-205 | L283 | Replace TestServerStartGracefulShutdown's fixed 6s sleep with a bounde | P2 | M | Sonnet-class | 5 |
| TASK-128 | CFG-AUDIT | Fix EnableRateLimit=false not actually disabling rate limiting | P2 | S | Sonnet-class | 2 |
| TASK-129 | L1957 | Fix wipeActivity dry-run count saturating at 2 | P2 | M | Sonnet-class | 2 |
| TASK-130 | L3384 | Register SearchIndexDroppedCount (and a dirty-backlog gauge) as Promet | P2 | S | Sonnet-class | 3 |
| TASK-131 | L3443 | Fix audiobook_organizer_books_total to report the true total, not just | P2 | S | Sonnet-class | 4 |
| TASK-132 | L4329 | Fix indexedStore.UpdateBook to enqueue a Bleve DELETE when the update  | P1 | S | Sonnet-class | 1 |
| TASK-133 | L4334 | Regression test: soft-deleting an indexed book must be unsearchable wi | P2 | S | Sonnet-class | 2 |
| TASK-134 | L4449 | Add a wiring-level test proving the server actually constructs CancelO | P2 | M | Sonnet-class | 1 |
| TASK-135 | L4575 | Convert metadata.batch-apply-cached from ResumeDrop to real checkpoint | P1 | M | Opus-class | 1 |
| TASK-136 | L4575 | Convert reconcile.apply from ResumeDrop to real checkpoint/resume | P1 | M | Opus-class | 1 |
| TASK-137 | L4732 | Fix TestOrganizeService_PerformOrganize_NoBooksToOrganize to mock the  | P2 | S | Sonnet-class | 1 |
| TASK-206 | TODO-SRVTIMEOUT | Split or speed up the internal/server test package -- migrate call sit | P2 | L | Opus-class | 1 |
| TASK-138 | ABS-SYNC | Exempt the ABS router group from the global BasicAuth() middleware | P1 | S | Sonnet-class | 1 |
| TASK-139 | ABS-SYNC | Prune expired abs_sess: records on the existing session-cleanup schedu | P2 | S | Haiku-class | 3 |
| TASK-140 | L10372 | Retire the unsafe cleanup_merged.go handler as a guarded no-op (owner  | P1 | S | Sonnet-class | 1 |
| TASK-141 | L10525 | Add regression tests for the 2 untested deluge hydrate sites | P2 | S | Haiku-class | 1 |
| TASK-208 | DEC-6 | Migrate internal/server test fixtures to setupTestServerWithStore — it | P2 | M | Sonnet-class | 1 |
| TASK-209 | DEC-6 | Migrate internal/server test fixtures to setupTestServerWithStore — it | P2 | M | Sonnet-class | 3 |
| TASK-210 | DEC-6 | Migrate internal/server test fixtures to setupTestServerWithStore — se | P2 | M | Sonnet-class | 1 |
| TASK-211 | DEC-6 | Migrate internal/server test fixtures to setupTestServerWithStore — co | P2 | M | Sonnet-class | 2 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  go build ./... && go vet ./... && go test ./internal/database/... ./internal/database/mocks/... ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/maintenance/jobs/... ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/metrics/... ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/... ./internal/server/handlers/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/middleware/... -count=1
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/metrics/metrics.go`: TASK-085, TASK-095, TASK-203, TASK-130, TASK-131 → serialize by wave (TASK-085=w1, TASK-095=w2, TASK-203=w5, TASK-130=w3, TASK-131=w4)
- `internal/metrics/metrics_test.go`: TASK-085, TASK-203, TASK-130, TASK-131 → serialize by wave (TASK-085=w1, TASK-203=w5, TASK-130=w3, TASK-131=w4)
- `internal/server/batch_apply_op.go`: TASK-096, TASK-135 → serialize by wave (TASK-096=w3, TASK-135=w1)
- `internal/server/handlers/operations_v2_test.go`: TASK-115, TASK-134 → serialize by wave (TASK-115=w2, TASK-134=w1)
- `internal/server/indexed_store_test.go`: TASK-132, TASK-133, TASK-209 → serialize by wave (TASK-132=w1, TASK-133=w2, TASK-209=w3)
- `internal/server/maintenance_fixups.go`: TASK-025, TASK-129 → serialize by wave (TASK-025=w1, TASK-129=w2)
- `internal/server/reconcile_ops.go`: TASK-096, TASK-136 → serialize by wave (TASK-096=w3, TASK-136=w1)
- `internal/server/server.go`: TASK-026, TASK-205 → serialize by wave (TASK-026=w1, TASK-205=w5)
- `internal/server/server_lifecycle.go`: TASK-026, TASK-065, TASK-205, TASK-128, TASK-131, TASK-139 → serialize by wave (TASK-026=w1, TASK-065=w6, TASK-205=w5, TASK-128=w2, TASK-131=w4, TASK-139=w3)
- `internal/server/server_more_test.go`: TASK-204, TASK-205 → serialize by wave (TASK-204=w1, TASK-205=w5)
- `internal/server/server_ops_store.go`: TASK-131, TASK-139 → serialize by wave (TASK-131=w4, TASK-139=w3)
- `internal/server/server_test.go`: TASK-206, TASK-211 → serialize by wave (TASK-206=w1, TASK-211=w2)
- `internal/server/wire_abs_routes.go`: TASK-127, TASK-212, TASK-156 → serialize by wave (TASK-127=w1, TASK-212=w3, TASK-156=w2)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-127, TASK-204, TASK-132, TASK-134, TASK-135, TASK-136, TASK-137, TASK-206, TASK-138, TASK-140, TASK-141, TASK-208, TASK-210 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-128, TASK-129, TASK-133, TASK-211 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 3 | TASK-130, TASK-139, TASK-209 | wave 2 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 4 | TASK-131 | wave 3 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 5 | TASK-205 | wave 4 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
