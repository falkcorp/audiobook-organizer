<!-- file: docs/agent-tasks/todo-completion-2026-09/search/README.md -->
<!-- version: 1.6.0 -->
<!-- guid: 3d65049e-f26b-48d6-82ae-703654519c93 -->
<!-- last-edited: 2026-09-10 -->

# Workstream — search (todo-completion-2026-09)

2 tasks: 2 carried forward from the 2026-08-21 package (ids kept), 0 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-125](TASK-125-index-track-names-on-bookdocument-so-smart-playl.md) | carried | correctness | P2 | M | Index track names on BookDocument so smart playlists can match them | internal/search/document.go: BookDocument struct (line 19) has no TrackNames field, 0 hits |
| [TASK-126](TASK-126-surface-to-the-user-when-all-and-or-any-stopword.md) | carried | hygiene | P2 | S | Surface to the user when 'all'/'and' (or any stopword) is silently dropped from  | internal/search/bleve_translator.go:166 dropStopwordOnlyConjuncts still drops conjuncts wi |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
