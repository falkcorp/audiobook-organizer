<!-- file: docs/agent-tasks/todo-completion/operations/TASK-124-distinguish-nothing-to-cancel-from-cancelled-in-.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2b7a3adf-9eb9-468a-adb9-b83c61d0f79e -->
<!-- last-edited: 2026-08-21 -->

# TASK-124 — Distinguish 'nothing to cancel' from 'cancelled' in registry.Cancel so unknown-id cancels 404 instead of lying 204 (TODO.md L4477)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · operations subagent · **Why:** Touches a shared registry method with 3 call sites across 2 packages, needs a sentinel-error design and careful test updates across both the wired and (currently dead) duplicate handler; not a one-line fix. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 4477 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Cancelling an operation the registry has never h" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-08.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/operations-124-distinguish-nothing-to-cancel-from-cancelled-in-" -b agent/operations-124-distinguish-nothing-to-cancel-from-cancelled-in- origin/main
cd "$REPO/.worktrees/operations-124-distinguish-nothing-to-cancel-from-cancelled-in-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Make Registry.Cancel distinguish 'an op with this id was found and actually signalled to stop' from 'no op with this id exists' by returning a sentinel/typed not-found error in the latter case, then have DELETE /api/v1/operations/v2/:id answer 404 for an unknown id and 204 only when something was actually cancelled or marked canceled. Also resolve (delete or fix) the dead duplicate handler in internal/server/operations_v2_handlers.go so it cannot silently diverge from the real fix.

## Background (verify before editing)

- Cancel has three branches (internal/operations/registry/registry.go:900-945): (1) running with a live cancel func -> real cancel, returns nil; (2) running-but-stub (claimed by dispatcher, not yet picked up) -> marks the DB row canceled, returns nil; (3) not running at all -> tries SetOperationV2StatusIfQueued and returns nil REGARDLESS of whether `updated` was true or false.
- Only branch (3)'s false case is the bug: it means 'this id was never queued or running at all' but is indistinguishable from 'it was queued and I just cancelled it.'
- Only handlers.OperationsV2Handler.CancelOperationV2 (internal/server/handlers/operations_v2.go:271) is actually wired to the route (internal/server/wire_operations_routes.go:26: `protected.DELETE("/operations/v2/:id", ..., opsV2H.CancelOperationV2)`). A second, near-identical implementation, Server.handleCancelOperationV2 (internal/server/operations_v2_handlers.go:133), exists but is NOT wired to any production route — it is referenced only by its own test file (internal/server/operations_v2_handlers_test.go:270,283), making it dead code that will silently diverge from the real fix if not addressed in the same change.
- internal/server/handlers/operations/handler.go:144-150 has a third, legacy caller (`CancelOperation`, DELETE /operations/:id) that already tolerates a Cancel error by falling through to a force-update — that fallback path is unaffected by this change and is out of scope (it is a different, still-serving legacy route being retired separately per other TODO items in this backlog).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func (r \*Registry) Cancel' internal/operations/registry/registry.go   # 1 hit at L900; tail of function (L928-945) returns nil regardless of `updated` — Registry.Cancel returns nil for an unknown id (no running handle, SetOperationV2StatusIfQueued updated=false)
  grep -n 'registry.Cancel(id)' internal/server/handlers/operations_v2.go   # 1 hit at L307, followed by RespondWithNoContent — the v2 handler responds 204 whenever Cancel returns nil
  grep -n "observed contract, not an endorsement" internal/server/server_extra_test.go   # 1 hit at L288 — a test already pins the 204-for-unknown-id behavior as a known defect, not a spec
  grep -rn 'handleCancelOperationV2' internal/server/   # hits in internal/server/operations_v2_handlers.go (definition, unused in production routing) and internal/server/operations_v2_handlers_test.go (only referenced by its own test) — there is a second, currently-DEAD duplicate handler with the same bug that must not be left to silently diverge
  ```

### Reuse — don't invent

- Use `httputil.RespondWithNotFound(c, resourceType, id)` in `internal/httputil/respond.go` (verify: `grep -n 'func RespondWithNotFound' internal/httputil/respond.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/operations/registry/registry.go, define a sentinel error near the top of the file: `var ErrOpNotFound = errors.New("operation not found")` (add "errors" import if not already present).
2. In Cancel (line ~928-945), change the final `return nil` to `if !updated { return ErrOpNotFound }` / `return nil` — i.e. only return nil when `updated` is true; return ErrOpNotFound when SetOperationV2StatusIfQueued reports no row was updated.
3. In internal/server/handlers/operations_v2.go's CancelOperationV2 (line 307-310), change the error handling: `if err := h.registry.Cancel(id); err != nil { if errors.Is(err, registry.ErrOpNotFound) { httputil.RespondWithNotFound(c, "operation", id); return }; httputil.InternalError(c, "cancel failed", err); return }` (add the registry package import and "errors" import).
4. Apply the same translation in internal/server/operations_v2_handlers.go's handleCancelOperationV2 (line 139-142) OR delete that function and its two references in internal/server/operations_v2_handlers_test.go entirely, since it duplicates functionality that is not reachable from production routing — prefer deletion (Fix It Right: remove the dead duplicate rather than patch code nothing calls) unless a maintainer confirms it is intentionally kept as a spare/future route.
5. Update TestOperationEndpointsErrors (internal/server/server_extra_test.go:289-292) to expect http.StatusNotFound instead of http.StatusNoContent for the unknown-id DELETE, and update its comment to describe the new, honest contract instead of the old 'observed contract, not an endorsement' caveat.
6. Check the frontend: web/src/services/api.ts's cancelOperation(id) (line 2119-2127) only throws on a non-ok HTTP response, so a 404 will now throw where a 204 silently succeeded before — confirm the UI's cancel-button call site (grep the caller of cancelOperation in web/src for a try/catch) handles a thrown error reasonably (e.g. shows 'this operation is no longer active' rather than an unhandled rejection) or file it as a small follow-up if the UI needs its own fix.
7. Bump version headers on every touched Go file.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_operations_124.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A genuinely running op with a stub handle (claimed by dispatcher, not yet picked up by a worker) must still return nil, not ErrOpNotFound — this is branch (2), untouched by this change; add a regression test asserting this stays 204.
- The legacy DELETE /operations/:id route's fallback-to-force-update behavior must be unaffected: it already tolerates any Cancel error (including the new ErrOpNotFound) by falling through, so no change needed there, but add a comment noting why it is left alone.

## Tests

- {'file': 'internal/operations/registry/registry_test.go', 'name': 'TestCancel_UnknownID_ReturnsErrOpNotFound (new)', 'asserts': "Cancel(unknownID) returns an error satisfying errors.Is(err, ErrOpNotFound), and a legitimately queued op's Cancel still returns nil"}
- {'file': 'internal/server/handlers/operations_v2_test.go', 'name': 'TestOperationsV2Handler_CancelOperationV2_UnknownID_Returns404 (new)', 'asserts': "CancelOperationV2 responds 404 (not 204) when the mock registry's Cancel returns registry.ErrOpNotFound"}
- {'file': 'internal/server/server_extra_test.go', 'name': 'TestOperationEndpointsErrors (modified)', 'asserts': 'DELETE /api/v1/operations/v2/bad-id now returns 404, not 204'}

Anti-over-suppression test: `N/A — this is an error-code fix, not a filter/guard.` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `go test ./internal/operations/registry/... ./internal/server/... -run 'Cancel'` passes with the new 404 expectation.
- [ ] `grep -rn 'handleCancelOperationV2' internal/server/` shows either zero hits (deleted) or a call site that is now actually wired into production routing (fixed, not merely left dead).
- [ ] Anti-over-suppression test: `N/A — this is an error-code fix, not a filter/guard.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_operations_124.md`.

## Commit message

```
refactor(operations): Distinguish 'nothing to cancel' from 'cancelled' in registry (TODO L4477)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Check whether the frontend UI shows a 'cancelled' confirmation toast on the old 204 that would now need to become an error toast on 404 — web/src/services/api.ts:2119 is the fetch wrapper; find its caller for the actual UI behavior before considering this fully done.
