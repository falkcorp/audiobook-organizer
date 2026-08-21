<!-- file: docs/agent-tasks/todo-completion/config/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 66126525-9dfc-4bf0-82fb-349b0909064e -->
<!-- last-edited: 2026-08-21 -->

# Workstream — config (todo-completion)

6 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-016 | L1247 | Rename write_back_metadata config key to auto_write_tags_on_fetch with | P1 | M | Opus-class | 6 |
| TASK-017 | CFG-AUDIT | Fix APIRateLimitPerMinute default drift between fresh-install (0) and  | P2 | S | Haiku-class | 1 |
| TASK-018 | CFG-AUDIT | Fix ai_backend.local_base_url hardcoded developer LAN IP default | P2 | S | Sonnet-class | 2 |
| TASK-019 | CFG-AUDIT | Fix ChapterConsolidationThresholdMin omitted from ResetToDefaults (fac | P2 | S | Haiku-class | 3 |
| TASK-020 | CFG-AUDIT | Delete the fully inert --enable-sqlite3-i-know-the-risks flag and Enab | P2 | M | Sonnet-class | 4 |
| TASK-021 | L10750 | Scan and fingerprint the assembled-source download root as a read-only | P1 | L | Opus-class | 7 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  go build ./... && go vet ./... && go test ./cmd/... ./internal/config/... ./internal/database/... ./internal/database/mocks/... -count=1 ; go build ./... && go vet ./... && go test ./internal/config/... -count=1 ; go build ./... && go vet ./... && go test ./internal/config/... ./internal/metafetch/... -count=1 ; go build ./... && go vet ./... && go test ./internal/config/... ./internal/plugins/acoustid/... ./internal/scanner/... -count=1
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/config/config.go`: TASK-016, TASK-017, TASK-018, TASK-019, TASK-020, TASK-021, TASK-070 → serialize by wave (TASK-016=w6, TASK-017=w1, TASK-018=w2, TASK-019=w3, TASK-020=w4, TASK-021=w7, TASK-070=w5)
- `internal/database/store.go`: TASK-020, TASK-031, TASK-033, TASK-037, TASK-039 → serialize by wave (TASK-020=w4, TASK-031=w1, TASK-033=w2, TASK-037=w6, TASK-039=w3)
- `internal/scanner/scanner.go`: TASK-021, TASK-181, TASK-106 → serialize by wave (TASK-021=w7, TASK-181=w2, TASK-106=w1)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-017 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-018 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 3 | TASK-019 | wave 2 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 4 | TASK-020 | wave 3 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 6 | TASK-016 | wave 5 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 7 | TASK-021 | wave 6 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
