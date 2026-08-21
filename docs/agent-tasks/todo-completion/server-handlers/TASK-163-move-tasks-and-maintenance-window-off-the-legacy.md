<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-163-move-tasks-and-maintenance-window-off-the-legacy.md -->
<!-- version: 1.0.0 -->
<!-- guid: 283e5465-d8d0-42cc-958d-59782d79982f -->
<!-- last-edited: 2026-08-21 -->

# TASK-163 — Move /tasks/* and /maintenance-window/* off the legacy v1 operations handler into their own scheduler-config handler (TODO.md L4563)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · server-handlers subagent · **Why:** A mechanical handler-extraction refactor (move 6 methods + their route registrations to a new package, thread the same scheduler-provider dependency) with enough surface area (6 handlers, their tests, and the wiring file) to need care but no novel design. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4563 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`/tasks/*` and `/maintenance-window/*` are NOT v" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-08.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-163-move-tasks-and-maintenance-window-off-the-legacy" -b agent/server-handlers-163-move-tasks-and-maintenance-window-off-the-legacy origin/main
cd "$REPO/.worktrees/server-handlers-163-move-tasks-and-maintenance-window-off-the-legacy"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Extract ListTasks, RunTask, UpdateTaskConfig, RunMaintenanceWindowNow, GetMaintenanceWindowStatus and UpdateMaintenanceWindowConfig out of internal/server/handlers/operations.Handler into a new, small handler (suggested package: internal/server/handlers/scheduler, or a new file within an existing package if a maintainer prefers not adding a package) so that retiring the v1 operations-record handler later does not read as 'delete task scheduling' — these 6 routes are scheduler configuration/control, not operation records, and should not be coupled to that retirement.

## Background (verify before editing)

- internal/server/handlers/operations/handler.go's doc comment (line 11) already self-describes as covering 'task-scheduler endpoints, and the maintenance-window endpoints' alongside the v1 operations-record surface — i.e. the file itself already flags this as three different concerns bundled together.
- The scheduler dependency is threaded lazily (lines 48-99, resolveScheduler) because *Server.scheduler is only assigned in Start(), which runs AFTER NewServer's handler-construction phase — any new handler needs the exact same lazy-provider shape, not a direct *scheduler.TaskScheduler field.
- This split does not change route paths, request/response shapes, or permissions (auth.PermSettingsManage is used uniformly across all 6 today) — it is purely an internal code-organization move.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n '"/tasks\|/maintenance-window' internal/server/wire_operations_routes.go   # 6 hits at L84-89, all calling operationsH.<Method> — all 6 routes are registered on operationsH, the legacy v1 operations handler
  grep -n 'func (h \*Handler) ListTasks\|func (h \*Handler) RunTask\|func (h \*Handler) UpdateTaskConfig\|func (h \*Handler) RunMaintenanceWindowNow\|func (h \*Handler) GetMaintenanceWindowStatus\|func (h \*Handler) UpdateMaintenanceWindowConfig' internal/server/handlers/operations/handler.go   # 6 hits at L457,467,663,752,768,798 — the 6 handler methods live in the same file/struct as the v1 operations-record CRUD being retired elsewhere
  grep -n 'getScheduler resolves the scheduler lazily' internal/server/handlers/operations/handler.go   # 1 hit around L48 — the handler already has a documented lazy-scheduler-provider pattern to reuse (scheduler is wired after these handlers are constructed)
  ```

### Reuse — don't invent

- Use `the lazy scheduler-provider closure pattern (Server.scheduler is assigned in Start(), after handler construction)` in `internal/server/handlers/operations/handler.go` (verify: `grep -n 'resolveScheduler' internal/server/handlers/operations/handler.go`) — do NOT write a parallel helper.

## Step-by-step

1. Create internal/server/handlers/scheduler_admin.go (or a new internal/server/handlers/scheduler/ package if the project's convention for other domains — audiobooks/, collections, metadata/ — is preferred; check whether existing single-purpose handlers in this dir are flat files or subpackages before choosing) with a new Handler type taking the same lazy scheduler-provider dependency as internal/server/handlers/operations.Handler.
2. Move ListTasks (operations/handler.go:457), RunTask (:467), UpdateTaskConfig (:663), RunMaintenanceWindowNow (:752), GetMaintenanceWindowStatus (:768) and UpdateMaintenanceWindowConfig (:798) verbatim into the new handler, along with any unexported helpers they exclusively use (e.g. fixedScheduleHint at line 561 if only these methods reference it — verify with grep before moving vs. leaving it behind).
3. Update internal/server/wire_operations_routes.go lines 84-89 to register the 6 routes against the new handler instance instead of operationsH.
4. Move the corresponding test cases out of internal/server/handlers/operations/handler_test.go (or wherever they currently live — grep for `func Test.*ListTasks\|func Test.*RunTask\|func Test.*MaintenanceWindow` under internal/server/handlers/operations/) into a new _test.go file alongside the new handler.
5. Bump version headers on every touched/created file.

Then, always:
- Keep the change purely removal — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_163.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Any other handler method in internal/server/handlers/operations/handler.go that calls ListTasks/RunTask/etc. internally (rather than only being reached via HTTP) needs its call site updated too — grep for internal callers before assuming this is purely route-registration surgery.

## Tests

- (none)

Anti-over-suppression: N/A

## How to test

```bash
make ci && npm --prefix web run lint && npm --prefix web test
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `grep -n '"/tasks\|/maintenance-window' internal/server/wire_operations_routes.go` shows the 6 routes registered against the NEW handler, not operationsH.
- [ ] `go build ./...` and `go test ./internal/server/...` both pass with no behavior change (same routes, same responses).
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci && npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_163.md`.

## Commit message

```
refactor(server-handlers): Move /tasks/* and /maintenance-window/* off the legacy v1 op (TODO L4563)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the removed symbol/file is already ABSENT at HEAD (re-run the re-verify greps above: zero hits = already removed) AND the replacement is present, the removal is already done — run acceptance instead. Rollback = `git revert` the commit to restore the file + its call sites; no data or schema is touched.

## Coordinator notes

This is purely an internal reorganization with no behavior change — keep the PR small and mechanical so it is trivially reviewable, and do not fold it into the same PR as the actual v1-operations-record retirement work happening elsewhere in this backlog.
