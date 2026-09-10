<!-- file: docs/agent-tasks/todo-completion-2026-09/audiobooks/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 43696005-d88c-47fe-998b-93e96f564593 -->
<!-- last-edited: 2026-09-10 -->

# Workstream — audiobooks (todo-completion-2026-09)

3 tasks: 3 carried forward from the 2026-08-21 package (ids kept), 0 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-001](TASK-001-add-a-short-ttl-cache-to-the-search-branch-of-ge.md) | carried | perf | P2 | M | Add a short-TTL cache to the search branch of GetAudiobooksWithTotal | internal/audiobooks/service_query.go:166 calls svc.searchWithBleve in the search branch wi |
| [TASK-004](TASK-004-add-a-conformance-test-asserting-the-library-pat.md) | carried | hygiene | P2 | S | Add a conformance test asserting the library path and author path classify nil/t | internal/audiobooks/service_query_isprimary_conformance_test.go still absent. Grepped IsPr |
| [TASK-005](TASK-005-wire-onlyparsedtranscription-style-filtering-int.md) | carried | correctness | P2 | M | Wire OnlyParsedTranscription-style filtering into the interactive audiobooks lis | OnlyParsedTranscription still 0 hits in internal/audiobooks/service_types.go (ListFilters  |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
