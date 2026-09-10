<!-- file: docs/agent-tasks/todo-completion-2026-09/activity/README.md -->
<!-- version: 1.7.0 -->
<!-- guid: edbb82bc-31d0-43db-89e8-9ecd384c12b3 -->
<!-- last-edited: 2026-09-10 -->

# Workstream — activity (todo-completion-2026-09)

1 tasks: 0 carried forward from the 2026-08-21 package (ids kept), 1 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-333](TASK-333-isbatchable-s-tier-gate-silently-routes-only-tie.md) | new-finding | hygiene | P3 | S | isBatchable's Tier gate silently routes only tier=debug high-volume entries thro | internal/activity/writer.go:176 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
