<!-- file: docs/agent-tasks/todo-completion-2026-09/config/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: eba2a243-73f8-4dcd-ae38-bd07fc3af48d -->
<!-- last-edited: 2026-09-10 -->

# Workstream — config (todo-completion-2026-09)

3 tasks: 2 carried forward from the 2026-08-21 package (ids kept), 1 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-016](TASK-016-rename-write-back-metadata-config-key-to-auto-wr.md) | carried | hygiene | P2 | M | Rename write_back_metadata config key to auto_write_tags_on_fetch with deprecate | grep 'auto_write_tags_on_fetch/AutoWriteTagsOnFetch' internal/config/ internal/metafetch/  |
| [TASK-020](TASK-020-delete-the-fully-inert-enable-sqlite3-i-know-the.md) | carried | hygiene | P2 | S | Delete the fully inert --enable-sqlite3-i-know-the-risks flag and EnableSQLite c | EnableSQLite still referenced throughout: cmd/diagnostics.go:76, cmd/dedup_bench.go:109, c |
| [TASK-344](TASK-344-mask-the-remaining-secrets-returned-by-get-api-v.md) | new-todo | security | P1 | M | Mask the remaining secrets returned by `GET /api/v1/config` | TODO.md lines 3344, 3345, 3346, 3347, 3348 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
