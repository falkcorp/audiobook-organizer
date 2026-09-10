<!-- file: docs/agent-tasks/todo-completion-2026-09/server-handlers/TASK-319-delete-operations-history-deletes-from-the-dead.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7e26ae98-8a72-4285-b93c-ab494b7a68f1 -->
<!-- last-edited: 2026-09-10 -->

# TASK-319 — DELETE /operations/history deletes from the dead v1 `operation:` keyspace; reports success while clearing nothing meaningful (SV-01)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `SV-01` (audit_server_handlers.json)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · server-handlers subagent · **Depends on:** none · **Wave:** 1 

Source: Wave 3 audit finding `SV-01` (audit_server_handlers.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/path/to/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-319" -b agent/server-handlers-319-delete-operations-history-deletes-from-t origin/main
cd "$REPO/.worktrees/server-handlers-319"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Either delete this endpoint alongside the other RETIRED v1 handlers (matching the pattern already used for GET /operations etc.), or repoint it at a v2 delete-by-status function so it clears operations_v2 rows.

Why it matters: A 200 response with a plausible 'deleted' count is exactly the shape that hides this: nothing errors, the count is just answering the wrong table. Re-wiring the UI's history-clear button to this endpoint (a reasonable thing for a future PR to do, since it already exists) would silently ship a no-op 'clear history' feature.

## Background (verify before editing)

- DeleteOperationHistory (handler.go:244-273) calls h.store.DeleteOperationsByStatus(statuses), which is internal/database/pebble_store_operations.go:368-399 -- it iterates the legacy `operation:` key prefix only. This file's own header comment (handler.go:1-22) and the routing comment in wire_operations_routes.go:33-46 document that the v1 operations minter was retired on 2026-08-23 and that GET /operations, /operations/:id/status and /operations/:id/logs were RETIRED for exactly this reason (183/200 rows permanently stuck at 'pending' on the v1 table, nothing else writes it). DeleteOperationHistory was never migrated alongside GetOperationResult (handler.go:477-490, which now reads h.store.GetOperationV2). The route is still wired at wire_operations_routes.go:78. `grep -rn deleteOperationHistory web/src` finds the api.ts client function (api.ts:2284) but zero call sites anywhere else in web/src -- the UI never invokes it today, so nothing is actively lying to a user right now, but any admin/API caller hitting DELETE /operations/history gets 200 {"deleted": N} implying the operation history was cleared when the actual history the app displays (GET /operations/timeline, operations_v2 keyspace) is untouched.
- Anchor: `internal/server/handlers/operations/handler.go:244` (audit `SV-01`, confidence high, severity medium).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -f internal/server/handlers/operations/handler.go   # the file the finding is anchored to still exists
  sed -n '238,250p' internal/server/handlers/operations/handler.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/server/handlers/operations/handler.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_server_handlers_319.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/handlers/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_server_handlers_319.md`.

## Commit message

```
fix(server-handlers): DELETE /operations/history deletes from the dead v1 `operation:` keysp (SV-01)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Pure code change: rollback = `git revert` the commit. If the re-verify greps show the fix already present, run acceptance instead of re-implementing.

## Coordinator notes

Standard lane: coordinator may admin-merge on a green gate.
