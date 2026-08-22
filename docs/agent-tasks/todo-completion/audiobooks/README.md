<!-- file: docs/agent-tasks/todo-completion/audiobooks/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 858e3f23-dcce-40af-855e-90a7145a2e53 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — audiobooks (todo-completion)

7 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-001 | SEARCH-CACHE | Add a short-TTL cache to the search branch of GetAudiobooksWithTotal ( | P1 | M | Opus-class | 3 |
| TASK-002 | L3348 | Fix the 3-way disagreement in how a nil IsPrimaryVersion is treated (m | P1 | M | Opus-class | 4 |
| TASK-176 | L3354 | Build a read-only census tool for the 41 ungrouped-but-explicitly-non- | P2 | S | Sonnet-class | 1 |
| TASK-190 | L3718 | Root-cause and fix: show_quarantined=true silently narrows the audiobo | P2 | L | Opus-class | 2 |
| TASK-003 | L3884 | Fix the author-path post-filter to treat nil IsPrimaryVersion as prima | P1 | S | Sonnet-class | 5 |
| TASK-004 | L3889 | Add a conformance test asserting the library path and author path clas | P1 | S | Sonnet-class | 6 |
| TASK-005 | L10728 | Wire OnlyParsedTranscription-style filtering into the interactive audi | P2 | M | Sonnet-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  go build ./... && go vet ./... && go test ./internal/audiobooks/... -count=1 ; go build ./... && go vet ./... && go test ./internal/audiobooks/... ./internal/database/... ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/audiobooks/... ./internal/server/handlers/audiobooks/... -count=1 ; go build ./... && go vet ./... && go test ./tools/cmd/orphan-nonprimary-census/... -count=1
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/audiobooks/service_filtering.go`: TASK-002, TASK-190, TASK-005, TASK-186 → serialize by wave (TASK-002=w4, TASK-190=w2, TASK-005=w1, TASK-186=w6)
- `internal/audiobooks/service_query.go`: TASK-001, TASK-002, TASK-190, TASK-003, TASK-005, TASK-186 → serialize by wave (TASK-001=w3, TASK-002=w4, TASK-190=w2, TASK-003=w5, TASK-005=w1, TASK-186=w6)
- `internal/audiobooks/service_query_test.go`: TASK-001, TASK-002 → serialize by wave (TASK-001=w3, TASK-002=w4)
- `internal/database/memdb_summaries.go`: TASK-190, TASK-026, TASK-039 → serialize by wave (TASK-190=w2, TASK-026=w1, TASK-039=w4)
- `internal/server/handlers/audiobooks/handler.go`: TASK-005, TASK-037, TASK-095, TASK-098 → serialize by wave (TASK-005=w1, TASK-037=w5, TASK-095=w2, TASK-098=w3)
- `internal/server/handlers/audiobooks/handler_test.go`: TASK-005, TASK-095 → serialize by wave (TASK-005=w1, TASK-095=w2)
- `internal/server/library_list_warmer.go`: TASK-190, TASK-186 → serialize by wave (TASK-190=w2, TASK-186=w6)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-176, TASK-005 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-190 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 3 | TASK-001 | wave 2 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 4 | TASK-002 | wave 3 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 5 | TASK-003 | wave 4 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 6 | TASK-004 | wave 5 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
