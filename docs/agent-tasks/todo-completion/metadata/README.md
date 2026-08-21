<!-- file: docs/agent-tasks/todo-completion/metadata/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 05c7efe1-bece-4dd4-afc3-e151800dbfd6 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — metadata (todo-completion)

3 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-082 | SCORE-REC | Route ScoreOneResultWithBreakdown's base==0 path through scoreRecorder | P2 | S | Sonnet-class | 1 |
| TASK-083 | SEC-CODEQL-BACKLOG | Assess the 2 critical go/request-forgery (SSRF) CodeQL alerts on cover | P1 | M | Opus-class | 5 |
| TASK-084 | L3517 | Prefix metadata-apply activity summaries with the book title and rende | P2 | S | Haiku-class | 2 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  make ci
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `changelog.d/20260821_missing-file-lane_089.md`: TASK-082, TASK-083 → serialize by wave (TASK-082=w1, TASK-083=w5)
- `internal/metafetch/service_apply.go`: TASK-074, TASK-075, TASK-084, TASK-124 → serialize by wave (TASK-074=w1, TASK-075=w3, TASK-084=w2, TASK-124=w4)
- `internal/server/handlers/abs/browse.go`: TASK-082, TASK-083, TASK-093, TASK-149 → serialize by wave (TASK-082=w1, TASK-083=w5, TASK-093=w2, TASK-149=w3)
- `internal/server/handlers/abs/browse_test.go`: TASK-082, TASK-083 → serialize by wave (TASK-082=w1, TASK-083=w5)
- `internal/server/handlers/abs/library_fake_test.go`: TASK-082, TASK-083, TASK-152 → serialize by wave (TASK-082=w1, TASK-083=w5, TASK-152=w4)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-082 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-084 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 5 | TASK-083 | wave 4 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
