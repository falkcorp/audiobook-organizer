<!-- file: docs/agent-tasks/todo-completion-2026-09/organize/README.md -->
<!-- version: 1.7.0 -->
<!-- guid: 0161a8d1-384f-458a-8ced-ff2b5666accd -->
<!-- last-edited: 2026-09-10 -->

# Workstream — organize (todo-completion-2026-09)

4 tasks: 3 carried forward from the 2026-08-21 package (ids kept), 1 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-121](TASK-121-make-resolveorganizedfilepath-s-plan-on-faith-fa.md) | carried | correctness | P2 | M | Make resolveOrganizedFilePath's plan-on-faith fallback loud and verify-before-wr | COORDINATOR OVERRIDE (direct re-verification, superseding shard_organize.json's STALE call |
| [TASK-122](TASK-122-add-an-edition-suffix-folder-pattern-token.md) | carried | correctness | P2 | S | Add an {edition_suffix} folder-pattern token (TODO.md L5021) | internal/organizer/pathbuild.go: `grep -n 'edition_suffix'` -> 0 hits, still. `{edition}`  |
| [TASK-203](TASK-203-add-a-detection-only-counter-structured-log-for-.md) | carried | perf | P2 | S | Add a detection-only counter + structured log for generateTargetPath path collis | `grep -rn 'prometheus.NewCounter' internal/organizer` -> 0 hits, still. OnCollision hook a |
| [TASK-303](TASK-303-single-file-organize-no-op-paths-report-success.md) | new-finding | data-loss | P1 | S | Single-file organize no-op paths report success without ever stat-verifying the  | internal/organizer/organizer.go:141 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
