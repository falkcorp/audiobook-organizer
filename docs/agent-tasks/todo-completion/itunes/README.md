<!-- file: docs/agent-tasks/todo-completion/itunes/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6411a580-8714-4aed-af7f-8bfc85e3ffe3 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — itunes (todo-completion)

7 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-184 | ITUNES-SMARTCRIT-PARSE | Measure iTunes XML track Persistent ID coverage against the local DB b | P2 | S | Sonnet-class | 1 |
| TASK-061 | ITUNES-SMARTCRIT-PARSE | Import the 224 materialized-Playlist-Items smart playlists as static s | P1 | M | Opus-class | 2 |
| TASK-185 | PLAYBACK-IMPORT | Report the iTunes listened/in-progress status pipeline's actual wiring | P2 | S | Sonnet-class | 1 |
| TASK-062 | PERF-5 | internal/itunes/backfill.go BackfillExternalIDs: replace offset pagina | P1 | M | Opus-class | 1 |
| TASK-063 | PERF-5 | internal/itunes/backfill.go BackfillITunesTrackPIDs: same offset-pagin | P1 | S | Sonnet-class | 2 |
| TASK-064 | REGROUP-PARTCHAPTER-PARSER | Add a Part->disc / Chapter->track filename parser so 'P0-C0'-style fol | P1 | M | Opus-class | 1 |
| TASK-065 | L10390 | P2 relocate-only sync cycle — the composed cycle already exists (RunRe | P1 | M | Opus-class | 6 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only' ; go build ./... && go vet ./... && go test ./cmd/pid-census/... ./internal/itunes/... -count=1 ; go build ./... && go vet ./... && go test ./internal/itunes/... -count=1 ; go build ./... && go vet ./... && go test ./internal/itunes/... ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/itunes/service/... -count=1 ; go build ./... && go vet ./... && go test ./internal/itunes/service/... ./internal/plugins/maintenance/... -count=1
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/itunes/backfill.go`: TASK-062, TASK-063 → serialize by wave (TASK-062=w1, TASK-063=w2)
- `internal/server/server_lifecycle.go`: TASK-026, TASK-065, TASK-205, TASK-128, TASK-131, TASK-139 → serialize by wave (TASK-026=w1, TASK-065=w6, TASK-205=w5, TASK-128=w2, TASK-131=w3, TASK-139=w4)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-184, TASK-185, TASK-062, TASK-064 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-061, TASK-063 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 6 | TASK-065 | wave 5 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
