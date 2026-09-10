<!-- file: docs/agent-tasks/todo-completion-2026-09/server-handlers/TASK-308-sse-handler-unconditionally-overrides-the-app-s.md -->
<!-- version: 1.6.0 -->
<!-- guid: 5d6955e4-6260-4f2c-bb7e-2c50838eb864 -->
<!-- last-edited: 2026-09-10 -->

# TASK-308 — SSE handler unconditionally overrides the app's restrictive CORS policy with Access-Control-Allow-Origin: * (SV-03)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `SV-03` (audit_server_handlers.json)

**Priority:** P3 · **Effort:** S · **Recommended subagent:** Opus-class · server-handlers subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: Wave 3 audit finding `SV-03` (audit_server_handlers.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-308" -b agent/server-handlers-308-sse-handler-unconditionally-overrides-th origin/main
cd "$REPO/.worktrees/server-handlers-308"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Delete the ACAO override in HandleSSE (events.go:221) and let corsMiddleware's already-installed, origin-checked header stand; or make HandleSSE itself echo the allowlisted origin rather than hardcoding "*".

Why it matters: The app's CORS posture elsewhere is a strict, Vary-respecting allowlist (server_middleware.go) explicitly chosen over a wildcard. This one route silently regresses that choice. Exploitability today is low (the endpoint still requires a valid session/bearer to read anything, and EventSource does not send credentials cross-origin by default), but it undermines the exact hardening the MED-2 fix was for and would become exploitable if credentials mode or the auth model changes without anyone re-checking this header.

## Background (verify before editing)

- EventHub.HandleSSE (events.go:216-222) sets `c.Header("Access-Control-Allow-Origin", "*")` unconditionally. GET /api/events is registered on s.router directly (server_lifecycle.go:1108-1109), and s.router already has `router.Use(corsMiddleware())` installed at construction time (server.go:471, called from NewServer at line 443, which runs before Start() registers /api/events) -- so corsMiddleware (server_middleware.go:54-90) runs first in the chain and sets a same-origin/allowlisted Access-Control-Allow-Origin plus Access-Control-Allow-Credentials: true for a matched Origin. Because gin.Context.Header() is a Set (not Add), HandleSSE's later call replaces that value with "*", while Access-Control-Allow-Credentials: true (set earlier and never reset) survives -- an invalid ACAO:*+credentials:true combination for any request a browser sends with credentials, and a wildcard-readable response for any that doesn't. /api/events is one of the two endpoints (with /metrics) that a 2026-09 pen-test finding (MED-2, cited in the surrounding server_lifecycle.go comment) specifically hardened to require auth precisely because it streams book/scan/metadata events to anonymous clients otherwise.
- Anchor: `internal/realtime/events.go:221` (audit `SV-03`, confidence high, severity low).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e internal/realtime/events.go   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '210,228p' internal/realtime/events.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '437,449p' internal/realtime/events.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/realtime/events.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_server_handlers_308.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if the fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving the dry-run / guard path writes nothing (fail-closed on error). A pure code change (lock, bound, check, propagated error) does not need this — do not add a dry-run surface to satisfy it.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/realtime/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_server_handlers_308.md`.

## Commit message

```
fix(server-handlers): SSE handler unconditionally overrides the app's restrictive CORS polic (SV-03)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
- **YES** — **`git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing; the PR is held for the owner.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
