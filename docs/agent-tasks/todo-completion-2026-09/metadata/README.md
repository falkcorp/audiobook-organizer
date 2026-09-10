<!-- file: docs/agent-tasks/todo-completion-2026-09/metadata/README.md -->
<!-- version: 1.6.0 -->
<!-- guid: 5a4c0671-b3d2-472e-bec6-4b1bfdf273b3 -->
<!-- last-edited: 2026-09-10 -->

# Workstream — metadata (todo-completion-2026-09)

3 tasks: 1 carried forward from the 2026-08-21 package (ids kept), 2 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-080](TASK-080-assess-the-2-critical-go-request-forgery-ssrf-co.md) | carried | security | P1 | M | Assess the 2 critical go/request-forgery (SSRF) CodeQL alerts on cover-fetch pat | Live re-verify at HEAD via `gh api /repos/falkcorp/audiobook-organizer/code-scanning/alert |
| [TASK-310](TASK-310-isbn-asin-enrichment-sweep-discards-every-provid.md) | new-finding | correctness | P0 | S | ISBN/ASIN enrichment sweep discards every provider search error, making a circui | internal/metafetch/isbn.go:399 |
| [TASK-317](TASK-317-auto-merge-primary-selection-match-4-silently-sc.md) | new-finding | correctness | P2 | S | Auto-merge primary-selection (MATCH-4) silently scores a book as having zero fil | internal/metafetch/service_apply.go:933 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
