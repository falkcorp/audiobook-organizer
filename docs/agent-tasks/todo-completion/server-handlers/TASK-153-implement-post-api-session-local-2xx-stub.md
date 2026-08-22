<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-153-implement-post-api-session-local-2xx-stub.md -->
<!-- version: 1.0.0 -->
<!-- guid: 79c68c67-9d91-44b6-8043-9724fe5dc24a -->
<!-- last-edited: 2026-08-21 -->

# TASK-153 — Implement POST /api/session/local (2xx stub) (TODO.md L4507)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · server-handlers subagent · **Why:** A single trivial route: authenticate, respond 200 with a non-empty body, per the spec's own note that Absorb tolerates 404/501 here and only AudioBooth needs a bare 2xx (no body parsing required for this half). · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4507 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`POST /api/session/local-all` 404s.** Observed f" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-08.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-153-implement-post-api-session-local-2xx-stub" -b agent/server-handlers-153-implement-post-api-session-local-2xx-stub origin/main
cd "$REPO/.worktrees/server-handlers-153-implement-post-api-session-local-2xx-stub"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add POST /api/session/local, returning an idempotent 2xx for any authenticated request, matching the spec's minimum bar (§1.8.8 item 1: 'ShelfPlayer sends it after every play/pause with maxAttempts:1, so a 404 immediately marks the connection offline'; §1.9.1 narrows this to 'still implement it, but a 404 is no longer fatal' for the two target clients).

## Background (verify before editing)

- This is the simpler sibling of /api/session/local-all (see part 2): the spec does not require this endpoint to persist anything beyond returning success, only that it never 404s so AudioBooth/Absorb do not mark the connection offline.
- internal/server/handlers/abs/play.go:356-419 (applySessionUpdate) establishes the project's existing 'always 200, never leak internal state via status code' convention for this exact API family — new handlers here should match it.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rn '"/api/session/' internal/server/handlers/abs/handler.go   # 2 hits, both for /api/session/:id/sync and /api/session/:id/close only — no route exists for /api/session/local or /api/session/local-all today
  grep -n '"/api/session/"' internal/server/wire_abs_routes.go   # 1 hit at L70 — /api/session/ is a reserved ABS path prefix (protects the route from silently matching a wrong handler)
  grep -n 'api/session/local' docs/specs/2026-07-29-abs-sync-api-design.md   # >=4 hits including §1.8.8 and §1.9.1 — a design spec already requires this endpoint and documents exact behavior
  grep -n 'session/local' docs/reference/abs-upstream-api-reference.md   # 1 hit, row marked ❌ — the upstream API reference tracks this as unimplemented
  ```

### Reuse — don't invent

- Use `respondPlainOK(c) — the idempotent-200 helper already used by applySessionUpdate for the same 'never 404, always succeed' contract` in `internal/server/handlers/abs/play.go` (verify: `grep -n 'func respondPlainOK' internal/server/handlers/abs/*.go`) — do NOT write a parallel helper.
- Use `servermiddleware.CurrentUser(c) — auth extraction pattern used by every other ABS write handler` in `internal/server/handlers/abs/play.go` (verify: `grep -n 'servermiddleware.CurrentUser' internal/server/handlers/abs/play.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/server/handlers/abs/play.go, add `func (h *Handler) SessionLocal(c *gin.Context) { defer respondPlainOK(c); _, ok := servermiddleware.CurrentUser(c); if !ok { return } }` (auth-gated but still always-200 on success; unauthenticated still gets whatever respondPlainOK sends since 401 handling for ABS routes should match the existing pattern elsewhere in this file — check what applySessionUpdate does on an auth failure and mirror it exactly rather than inventing a new convention).
2. Register the route in internal/server/handlers/abs/handler.go's Register method, alongside the existing `/api/session/:id/sync` and `/api/session/:id/close` registrations (around line 534-535): `r.POST("/api/session/local", auth, h.SessionLocal)`.
3. Add the new route path to the documented route list near internal/server/wire_abs_routes.go:636-637 (the comment block listing implemented /api/session/ routes) so the reserved-prefix documentation stays accurate.
4. Bump version headers on both touched files.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_153.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Unauthenticated request: match whatever applySessionUpdate does today (verify at internal/server/handlers/abs/play.go:370-373) rather than assuming 401 is correct — the spec's 'never 404' concern is about connectivity probing, not necessarily about auth.

## Tests

- {'file': 'internal/server/handlers/abs/play_test.go', 'name': 'TestSessionLocal_ReturnsOK (new)', 'asserts': 'POST /api/session/local as an authenticated user returns 2xx with a non-empty body'}

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `curl -X POST /api/session/local` (authenticated) returns 2xx, not 404.
- [ ] `go test ./internal/server/handlers/abs/... -run TestSessionLocal` passes.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_153.md`.

## Commit message

```
feat(server-handlers): Implement POST /api/session/local (2xx stub) (TODO L4507)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (``curl -X POST /api/session/local` (authenticated) returns 2xx, not 404.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

See part 2 (same todo_line) for the more substantial /api/session/local-all endpoint, which actually applies progress.
