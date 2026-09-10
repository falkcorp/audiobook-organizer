<!-- file: docs/agent-tasks/todo-completion-2026-09/ci-tooling/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: a805f422-4489-4f9e-8680-47904df6f300 -->
<!-- last-edited: 2026-09-10 -->

# Workstream — ci-tooling (todo-completion-2026-09)

11 tasks: 3 carried forward from the 2026-08-21 package (ids kept), 8 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-009](TASK-009-teach-the-abs-fixture-capture-harness-to-record-.md) | carried | hygiene | P2 | M | Teach the ABS fixture-capture harness to record request headers | scripts/abs_capture_fixtures.py exists; grep 'request_headers/RequestHeaders/req_headers'  |
| [TASK-014](TASK-014-remove-committed-mtls-bridge-build-artifact-and-.md) | carried | hygiene | P2 | S | Remove committed mtls-bridge build artifact and gitignore it | git ls-files -s mtls-bridge = '100755 05891c026efd664175d674d8bd4a19364f624c05 0 mtls-brid |
| [TASK-191](TASK-191-bump-the-github-common-reusable-workflow-pins-in.md) | carried | hygiene | P2 | S | Bump the github-common reusable-workflow pins in at least two PRs, low-consequen | Identical state to 09-02: 6 workflows (security.yml, nightly-burndown.yml, nightly.yml, ha |
| [TASK-307](TASK-307-frontend-ci-yml-grants-an-unjustified-broad-perm.md) | new-finding | security | P1 | S | frontend-ci.yml grants an unjustified, broad permission ceiling to an external r | .github/workflows/frontend-ci.yml:20 |
| [TASK-311](TASK-311-deploy-deploy-debug-guard-checks-not-behind-inst.md) | new-finding | correctness | P1 | S | deploy/deploy-debug guard checks 'not behind' instead of 'exactly equals' origin | Makefile.local.example:72 |
| [TASK-312](TASK-312-node-version-drift-security-yml-pins-node-20-x-f.md) | new-finding | correctness | P2 | S | Node version drift: security.yml pins Node 20.x for npm dependency submission wh | .github/workflows/security.yml:77 |
| [TASK-313](TASK-313-the-only-go-version-consistency-check-truncates.md) | new-finding | correctness | P2 | S | The only Go-version consistency check truncates to major.minor, never checks .en | .github/workflows/test-action-integration.yml:105 |
| [TASK-314](TASK-314-no-concurrency-guard-between-the-two-burndown-di.md) | new-finding | correctness | P2 | S | No concurrency guard between the two burndown-dispatch workflows sharing the sam | .github/workflows/hard-burndown.yml:29 |
| [TASK-320](TASK-320-frontend-job-gate-is-a-computed-if-that-can-sile.md) | new-finding | correctness | P3 | S | frontend job gate is a computed `if:` that can silently skip a required-looking  | .github/workflows/frontend-ci.yml:54 |
| [TASK-339](TASK-339-real-reflink-detection-via-zdb-dva-comparison-lo.md) | new-todo | security | P1 | M | Real reflink detection via zdb DVA comparison (LOW PRIORITY, 2026-09-07) | TODO.md lines 1935 |
| [TASK-360](TASK-360-c716-resolved-the-3-954-book-api-vs-store-gap-de.md) | new-todo | security | P1 | M | C716 resolved: the "3,954-book API-vs-store gap" decomposes to 3,953 instrument  | TODO.md lines 10432 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
