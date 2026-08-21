<!-- file: docs/agent-tasks/todo-completion/metadata/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: dbd99404-a31b-4617-9523-38fa6c043c5f -->
<!-- last-edited: 2026-08-21 -->

# Workstream — metadata (todo-completion)

3 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-087 | SCORE-REC | Route ScoreOneResultWithBreakdown's base==0 path through scoreRecorder | P2 | S | Sonnet-class | 1 |
| TASK-088 | SEC-CODEQL-BACKLOG | Assess the 2 critical go/request-forgery (SSRF) CodeQL alerts on cover | P1 | M | Opus-class | 2 |
| TASK-089 | L3517 | Prefix metadata-apply activity summaries with the book title and rende | P2 | S | Haiku-class | 3 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  make ci
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `changelog.d/20260821_missing-file-lane_089.md`: TASK-087, TASK-088 → serialize by wave (TASK-087=w1, TASK-088=w2)
- `internal/metafetch/service_apply.go`: TASK-079, TASK-080, TASK-089, TASK-129 → serialize by wave (TASK-079=w1, TASK-080=w2, TASK-089=w3, TASK-129=w4)
- `internal/server/handlers/abs/browse.go`: TASK-087, TASK-088, TASK-098, TASK-154 → serialize by wave (TASK-087=w1, TASK-088=w2, TASK-098=w3, TASK-154=w4)
- `internal/server/handlers/abs/browse_test.go`: TASK-087, TASK-088 → serialize by wave (TASK-087=w1, TASK-088=w2)
- `internal/server/handlers/abs/library_fake_test.go`: TASK-087, TASK-088, TASK-157 → serialize by wave (TASK-087=w1, TASK-088=w2, TASK-157=w5)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-087 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-088 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 3 | TASK-089 | wave 2 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
