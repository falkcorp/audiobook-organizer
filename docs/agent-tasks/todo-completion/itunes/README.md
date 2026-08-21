<!-- file: docs/agent-tasks/todo-completion/itunes/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8b8da68f-0ab8-4494-bea6-9c6387b95141 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — itunes (todo-completion)

7 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-067 | ITUNES-SMARTCRIT-PARSE | Measure iTunes XML track Persistent ID coverage against the local DB b | P2 | S | Sonnet-class | 1 |
| TASK-068 | ITUNES-SMARTCRIT-PARSE | Import the 224 materialized-Playlist-Items smart playlists as static s | P1 | M | Opus-class | 2 |
| TASK-069 | PLAYBACK-IMPORT | Report the iTunes listened/in-progress status pipeline's actual wiring | P2 | S | Sonnet-class | 1 |
| TASK-070 | PERF-5 | internal/itunes/backfill.go BackfillExternalIDs: replace offset pagina | P1 | M | Opus-class | 1 |
| TASK-071 | PERF-5 | internal/itunes/backfill.go BackfillITunesTrackPIDs: same offset-pagin | P1 | S | Sonnet-class | 2 |
| TASK-072 | REGROUP-PARTCHAPTER-PARSER | Add a Part->disc / Chapter->track filename parser so 'P0-C0'-style fol | P1 | M | Opus-class | 1 |
| TASK-073 | L10390 | P2 relocate-only sync cycle — the composed cycle already exists (RunRe | P1 | M | Opus-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only' ; make ci
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/itunes/backfill.go`: TASK-070, TASK-071 → serialize by wave (TASK-070=w1, TASK-071=w2)
- `internal/server/server_lifecycle.go`: TASK-028, TASK-073, TASK-137, TASK-141, TASK-149 → serialize by wave (TASK-028=w2, TASK-073=w1, TASK-137=w3, TASK-141=w4, TASK-149=w5)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-067, TASK-069, TASK-070, TASK-072, TASK-073 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-068, TASK-071 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
