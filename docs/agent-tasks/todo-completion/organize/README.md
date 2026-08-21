<!-- file: docs/agent-tasks/todo-completion/organize/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2b87b1a6-6abc-4249-be95-0fb47a685b39 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — organize (todo-completion)

4 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-119 | F5 | Replace the size-equality heuristic in OrganizeBookDirectory's destina | P1 | S | Sonnet-class | 1 |
| TASK-120 | F5 | Route the three organize/rename paths through organizer.MoveBookFile's | P1 | L | Opus-class | 2 |
| TASK-121 | L4919 | Make resolveOrganizedFilePath's plan-on-faith fallback loud and verify | P1 | M | Opus-class | 1 |
| TASK-122 | L5021 | Add an {edition_suffix} folder-pattern token | P1 | S | Sonnet-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  go build ./... && go vet ./... && go test ./internal/metafetch/... ./internal/organizer/... -count=1 ; go build ./... && go vet ./... && go test ./internal/organizer/... -count=1
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/metafetch/service_apply.go`: TASK-081, TASK-120 → serialize by wave (TASK-081=w1, TASK-120=w2)
- `internal/organizer/organizer.go`: TASK-119, TASK-120 → serialize by wave (TASK-119=w1, TASK-120=w2)
- `internal/organizer/service.go`: TASK-186, TASK-121 → serialize by wave (TASK-186=w5, TASK-121=w1)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-119, TASK-121, TASK-122 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-120 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
