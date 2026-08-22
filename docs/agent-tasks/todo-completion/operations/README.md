<!-- file: docs/agent-tasks/todo-completion/operations/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: e65cb2dd-0acd-48e8-bab2-a753f9e6c035 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — operations (todo-completion)

4 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-115 | L4477 | Distinguish 'nothing to cancel' from 'cancelled' in registry.Cancel so | P2 | M | Sonnet-class | 2 |
| TASK-116 | L4586 | Forward IsCanceled() through reporterLogger to the ops registry's canc | P1 | M | Opus-class | 1 |
| TASK-117 | L4703 | Give prodSchedulerStore an Unwrap() so capability lookups can see past | P2 | S | Sonnet-class | 1 |
| TASK-118 | L4743 | Delete internal/operations/mocks — its only referencer is dead, perman | P2 | S | Sonnet-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  go build ./... && go vet ./... && go test ./internal/operations/... ./internal/scanner/... -count=1 ; go build ./... && go vet ./... && go test ./internal/operations/mocks/... ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/operations/registry/... -count=1 ; go build ./... && go vet ./... && go test ./internal/operations/registry/... ./internal/server/... ./internal/server/handlers/... -count=1
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `.mockery.yaml`: TASK-118, TASK-123 → serialize by wave (TASK-118=w1, TASK-123=w2)
- `internal/operations/registry/registry.go`: TASK-096, TASK-113, TASK-115 → serialize by wave (TASK-096=w3, TASK-113=w1, TASK-115=w2)
- `internal/operations/registry/registry_test.go`: TASK-096, TASK-115 → serialize by wave (TASK-096=w3, TASK-115=w2)
- `internal/server/handlers/operations_v2_test.go`: TASK-115, TASK-134 → serialize by wave (TASK-115=w2, TASK-134=w1)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-116, TASK-117, TASK-118 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-115 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
