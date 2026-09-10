<!-- file: docs/agent-tasks/todo-completion-2026-09/docs/README.md -->
<!-- version: 1.7.0 -->
<!-- guid: 389944c4-1ffa-46f8-80b5-19157a5e7eb4 -->
<!-- last-edited: 2026-09-10 -->

# Workstream — docs (todo-completion-2026-09)

2 tasks: 2 carried forward from the 2026-08-21 package (ids kept), 0 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-057](TASK-057-phase-8-write-the-abs-topology-runbook-and-migra.md) | carried | hygiene | P2 | M | Phase 8 -- write the ABS topology, runbook, and migration guide | docs/reference/abs-sync-topology-runbook.md still absent; docs/reference/ holds only abs-c |
| [TASK-182](TASK-182-record-the-docs-system-vs-top-level-architecture.md) | carried | hygiene | P2 | S | Record the docs/system vs top-level architecture classification decision in the  | docs/audits/2026-08-11-docs-inventory.md:224 still reads 'out of the classification scope. |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
