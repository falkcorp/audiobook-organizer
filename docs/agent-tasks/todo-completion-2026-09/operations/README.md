<!-- file: docs/agent-tasks/todo-completion-2026-09/operations/README.md -->
<!-- version: 1.6.0 -->
<!-- guid: 701cd3da-063f-4c8f-b62b-0906e410564a -->
<!-- last-edited: 2026-09-10 -->

# Workstream — operations (todo-completion-2026-09)

5 tasks: 3 carried forward from the 2026-08-21 package (ids kept), 2 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-116](TASK-116-forward-iscanceled-through-reporterlogger-to-the.md) | carried | correctness | P2 | M | Forward IsCanceled() through reporterLogger to the ops registry's cancellation s | internal/operations/progress.go: `grep -n 'func (l \*reporterLogger)'` still returns only  |
| [TASK-117](TASK-117-give-prodschedulerstore-an-unwrap-so-capability-.md) | carried | hygiene | P2 | S | Give prodSchedulerStore an Unwrap() so capability lookups can see past it (TODO. | internal/operations/registry/register.go:58-60 `type prodSchedulerStore struct { opRegistr |
| [TASK-118](TASK-118-delete-internal-operations-mocks-its-only-refere.md) | carried | hygiene | P2 | S | Delete internal/operations/mocks — its only referencer is dead, permanently-unta | internal/operations/mocks/mock_progress_reporter.go still exists (206 lines). `grep -rln ' |
| [TASK-316](TASK-316-resumerestart-proceeds-to-announce-an-op-as-queu.md) | new-finding | correctness | P2 | S | resumeRestart proceeds to announce an op as "queued" (publishOpCreated + pingDis | internal/operations/registry/resume.go:265 |
| [TASK-367](TASK-367-operationdef-permissions-is-enforced-by-nothing.md) | new-todo | security | P1 | M | `OperationDef.Permissions` is enforced by nothing — and PR-3 is about to delete  | TODO.md lines 11964 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
