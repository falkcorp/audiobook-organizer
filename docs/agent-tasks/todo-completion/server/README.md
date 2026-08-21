<!-- file: docs/agent-tasks/todo-completion/server/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9e582ff1-d8bf-4efd-b971-d20a1f30d696 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — server (todo-completion)

16 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-136 | N-11 | Log ABS_API_ENABLED's actual boot-time value unconditionally (currentl | P2 | S | Haiku-class | 1 |
| TASK-137 | CFG-AUDIT | Fix EnableRateLimit=false not actually disabling rate limiting | P2 | S | Sonnet-class | 3 |
| TASK-138 | L1957 | Fix wipeActivity dry-run count saturating at 2 | P2 | M | Sonnet-class | 2 |
| TASK-139 | UI-LOCKUP-2 | Reproduce and classify the persistent UI lockup (backend vs frontend v | P2 | M | Sonnet-class | 1 |
| TASK-140 | L3384 | Register SearchIndexDroppedCount (and a dirty-backlog gauge) as Promet | P2 | S | Sonnet-class | 2 |
| TASK-141 | L3443 | Fix audiobook_organizer_books_total to report the true total, not just | P2 | S | Sonnet-class | 4 |
| TASK-142 | L4329 | Fix indexedStore.UpdateBook to enqueue a Bleve DELETE when the update  | P1 | S | Sonnet-class | 2 |
| TASK-143 | L4334 | Regression test: soft-deleting an indexed book must be unsearchable wi | P1 | S | Sonnet-class | 3 |
| TASK-144 | L4449 | Add a wiring-level test proving the server actually constructs CancelO | P2 | M | Sonnet-class | 1 |
| TASK-145 | L4575 | Convert metadata.batch-apply-cached from ResumeDrop to real checkpoint | P1 | M | Opus-class | 1 |
| TASK-146 | L4575 | Convert reconcile.apply from ResumeDrop to real checkpoint/resume | P1 | M | Opus-class | 1 |
| TASK-147 | L4732 | Fix TestOrganizeService_PerformOrganize_NoBooksToOrganize to mock the  | P2 | S | Haiku-class | 1 |
| TASK-148 | ABS-SYNC | Exempt the ABS router group from the global BasicAuth() middleware | P1 | S | Sonnet-class | 1 |
| TASK-149 | ABS-SYNC | Prune expired abs_sess: records on the existing session-cleanup schedu | P2 | S | Haiku-class | 5 |
| TASK-150 | L10372 | Retire the unsafe cleanup_merged.go handler as a guarded no-op (owner  | P1 | S | Sonnet-class | 1 |
| TASK-151 | L10525 | Add regression tests for the 2 untested deluge hydrate sites | P2 | S | Haiku-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only' ; make ci
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/metrics/metrics.go`: TASK-094, TASK-140, TASK-141 → serialize by wave (TASK-094=w1, TASK-140=w2, TASK-141=w4)
- `internal/server/indexed_store.go`: TASK-024, TASK-142 → serialize by wave (TASK-024=w1, TASK-142=w2)
- `internal/server/maintenance_fixups.go`: TASK-027, TASK-138 → serialize by wave (TASK-027=w1, TASK-138=w2)
- `internal/server/server_lifecycle.go`: TASK-028, TASK-073, TASK-137, TASK-141, TASK-149 → serialize by wave (TASK-028=w2, TASK-073=w1, TASK-137=w3, TASK-141=w4, TASK-149=w5)
- `internal/server/wire_abs_routes.go`: TASK-136, TASK-164 → serialize by wave (TASK-136=w1, TASK-164=w2)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-136, TASK-139, TASK-144, TASK-145, TASK-146, TASK-147, TASK-148, TASK-150, TASK-151 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-138, TASK-140, TASK-142 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 3 | TASK-137, TASK-143 | wave 2 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 4 | TASK-141 | wave 3 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 5 | TASK-149 | wave 4 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
