<!-- file: docs/agent-tasks/todo-completion/config/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: b252cdc4-0aa4-4c51-a1a5-030ec77169d5 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — config (todo-completion)

5 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-016 | L1247 | Rename write_back_metadata config key to auto_write_tags_on_fetch with | P1 | M | Opus-class | 4 |
| TASK-017 | CFG-AUDIT | Fix APIRateLimitPerMinute default drift between fresh-install (0) and  | P2 | S | Haiku-class | 1 |
| TASK-018 | CFG-AUDIT | Fix ai_backend.local_base_url hardcoded developer LAN IP default | P1 | S | Sonnet-class | 5 |
| TASK-019 | CFG-AUDIT | Fix ChapterConsolidationThresholdMin omitted from ResetToDefaults (fac | P2 | S | Haiku-class | 2 |
| TASK-020 | CFG-AUDIT | Delete the fully inert --enable-sqlite3-i-know-the-risks flag and Enab | P2 | M | Sonnet-class | 3 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  go build ./... && go vet ./... && go test ./cmd/... ./internal/config/... ./internal/database/... ./internal/database/mocks/... -count=1 ; go build ./... && go vet ./... && go test ./internal/config/... -count=1 ; go build ./... && go vet ./... && go test ./internal/config/... ./internal/metafetch/... -count=1
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/config/config.go`: TASK-016, TASK-017, TASK-018, TASK-019, TASK-020, TASK-193, TASK-070 → serialize by wave (TASK-016=w4, TASK-017=w1, TASK-018=w5, TASK-019=w2, TASK-020=w3, TASK-193=w7, TASK-070=w6)
- `internal/config/config_test.go`: TASK-017, TASK-018, TASK-019, TASK-020 → serialize by wave (TASK-017=w1, TASK-018=w5, TASK-019=w2, TASK-020=w3)
- `internal/config/config_unit_test.go`: TASK-016, TASK-020 → serialize by wave (TASK-016=w4, TASK-020=w3)
- `internal/database/store.go`: TASK-020, TASK-031, TASK-033, TASK-037, TASK-039 → serialize by wave (TASK-020=w3, TASK-031=w1, TASK-033=w2, TASK-037=w5, TASK-039=w4)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-017 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-019 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 3 | TASK-020 | wave 2 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 4 | TASK-016 | wave 3 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 5 | TASK-018 | wave 4 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
