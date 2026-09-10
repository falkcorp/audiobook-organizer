<!-- file: docs/agent-tasks/todo-completion/metadata/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: f2b0dba8-d91b-4825-8eec-6c016747959a -->
<!-- last-edited: 2026-08-21 -->

# Workstream — metadata (todo-completion)

5 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-079 | SCORE-REC | Route ScoreOneResultWithBreakdown's base==0 path through scoreRecorder | P2 | S | Sonnet-class | 1 |
| TASK-080 | SEC-CODEQL-BACKLOG | Assess the 2 critical go/request-forgery (SSRF) CodeQL alerts on cover | P1 | M | Opus-class | 1 |
| TASK-081 | L3517 | Prefix metadata-apply activity summaries with the book title and rende | P2 | S | Haiku-class | 1 |
| TASK-196 | L4081 | Build an async, fanned-out background operation for metadata matching  | P2 | L | Opus-class | 1 |
| TASK-221 | DUPROW-1 | metafetch: collapse duplicate book_file rows by cleaned path before wr | P1 | S | Sonnet-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  go build ./... && go vet ./... && go test ./internal/covers/... ./internal/metadata/... -count=1 ; go build ./... && go vet ./... && go test ./internal/metafetch/... -count=1 ; go build ./... && go vet ./... && go test ./internal/metafetch/... ./internal/server/... -count=1 && npm --prefix web run lint && npm --prefix web test
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `web/src/components/audiobooks/BulkMetadataSearchDialog.tsx`: TASK-196, TASK-165 → serialize by wave (TASK-196=w1, TASK-165=w7)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-079, TASK-080, TASK-081, TASK-196, TASK-221 | none | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
