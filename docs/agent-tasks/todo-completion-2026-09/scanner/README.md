<!-- file: docs/agent-tasks/todo-completion-2026-09/scanner/README.md -->
<!-- version: 1.6.0 -->
<!-- guid: 16f00fc4-3c51-416e-881d-27e2153f3494 -->
<!-- last-edited: 2026-09-10 -->

# Workstream — scanner (todo-completion-2026-09)

2 tasks: 0 carried forward from the 2026-08-21 package (ids kept), 2 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-309](TASK-309-inline-ai-parse-phase-result-is-discarded-by-the.md) | new-finding | correctness | P0 | S | Inline AI-parse phase result is discarded by the scan, so a fully-aborted LLM ph | internal/scanner/scanner.go:1705 |
| [TASK-351](TASK-351-stage-3-durable-deferral-when-no-rung-answers-th.md) | new-todo | correctness | P1 | L | Stage 3 — durable deferral — When no rung answers, the candidates are currently  | TODO.md lines 3842 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
