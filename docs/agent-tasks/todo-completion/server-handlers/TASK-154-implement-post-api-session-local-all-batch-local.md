<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-154-implement-post-api-session-local-all-batch-local.md -->
<!-- version: 1.0.0 -->
<!-- guid: b84e86d7-8bc6-47b0-8f51-e4ccc70b9d26 -->
<!-- last-edited: 2026-08-21 -->

# TASK-154 — Implement POST /api/session/local-all (batch local-session sync, accept both body shapes) (TODO.md L4507)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · server-handlers subagent · **Why:** Needs a dual-shape JSON decode (object-with-sessions-key vs bare array) plus mapping ABS 'local session' fields onto the existing progressPatchRequest/applyProgressUpdate machinery — more judgment than a pure copy-paste of MediaProgressBatchUpdate. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 4507 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`POST /api/session/local-all` 404s.** Observed f" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-08.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-154-implement-post-api-session-local-all-batch-local" -b agent/server-handlers-154-implement-post-api-session-local-all-batch-local origin/main
cd "$REPO/.worktrees/server-handlers-154-implement-post-api-session-local-all-batch-local"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add POST /api/session/local-all: accept a body that is EITHER a bare JSON array of local playback sessions OR an object shaped {"sessions": [...], "deviceInfo": {...}} (AudioBooth's shape), apply each session's progress via the existing resolveBookID + applyProgressUpdate pipeline (mirroring MediaProgressBatchUpdate's loop exactly), silently skip sessions naming an unknown item id, and always answer 200 — never a 4xx, per the spec's explicit warning that a 4xx here permanently wedges the client's offline replay queue.

## Background (verify before editing)

- A 'local session' in the ABS local-sync API represents an offline listening session a client recorded while disconnected and is now uploading; structurally it carries the same currentTime/duration/timeListened fields as the live /sync body (internal/server/handlers/abs/play.go's syncRequest, lines 335-340) plus an item identifier and (for AudioBooth) deviceInfo.
- MediaProgressBatchUpdate (internal/server/handlers/abs/progress.go:157-184) already implements the identical shape for a DIFFERENT route (PATCH /api/me/progress/batch/update): bare array body, per-item resolveBookID + applyProgressUpdate, skip on unknown id, single final respondPlainOK — this is the template to copy, not invent from scratch.
- The two required body shapes are genuinely different at the JSON level (array vs. object-wrapping-an-array), so the handler needs to peek at the raw body and branch, or unmarshal into a struct with a custom UnmarshalJSON that accepts both — a custom UnmarshalJSON on a wrapper type is the cleaner approach and avoids double-parsing.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'local-all.*must return 200' docs/specs/2026-07-29-abs-sync-api-design.md   # 1 hit around L173 — spec mandates 200-and-ignore-unknown-IDs for local-all
  grep -n 'body shape differs by client' docs/specs/2026-07-29-abs-sync-api-design.md   # 1 hit around L487 — spec mandates accepting both an object-with-sessions-key and a bare-array body
  grep -n 'func (h \*Handler) MediaProgressBatchUpdate' internal/server/handlers/abs/progress.go   # 1 hit at L157 — an existing handler already implements the exact per-item pattern needed: bare array, resolveBookID + applyProgressUpdate, skip unknown, always 200
  ```

### Reuse — don't invent

- Use `MediaProgressBatchUpdate's per-item loop shape (resolveBookID, applyProgressUpdate, skip-unknown, always respondPlainOK)` in `internal/server/handlers/abs/progress.go` (verify: `grep -n 'func (h \*Handler) MediaProgressBatchUpdate' internal/server/handlers/abs/progress.go`) — do NOT write a parallel helper.
- Use `h.resolveBookID(userID, raw) — resolves a client-sent id (libraryItemId or sync id) to an internal bookID` in `internal/server/handlers/abs/progress.go` (verify: `grep -n 'func (h \*Handler) resolveBookID' internal/server/handlers/abs/progress.go`) — do NOT write a parallel helper.
- Use `h.applyProgressUpdate(userID, bookID, req progressPatchRequest) error` in `internal/server/handlers/abs/progress.go` (verify: `grep -n 'func (h \*Handler) applyProgressUpdate' internal/server/handlers/abs/progress.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/server/handlers/abs/play.go, define a `localSession` struct with the fields the spec requires per §1.8.8 (item id, currentTime, duration, timeListened — reuse the same *float64 pointer-optional pattern as syncRequest at play.go:335-340, since absent-vs-zero matters here too).
2. Define a wrapper type, e.g. `localSessionBatch struct { Sessions []localSession }`, with a custom `UnmarshalJSON([]byte) error` that first tries decoding as a bare `[]localSession` (abs-shim's shape), and on failure tries decoding as `struct{ Sessions []localSession \`json:"sessions"\`; DeviceInfo map[string]any \`json:"deviceInfo"\` }` (AudioBooth's shape) — assign whichever succeeds into Sessions.
3. Add `func (h *Handler) SessionLocalAll(c *gin.Context)`: `defer respondPlainOK(c)`; get the user via servermiddleware.CurrentUser (return early, still via the deferred 200, on failure — matching applySessionUpdate's own auth-failure-is-still-200 convention, verify this is actually its convention before copying it); `c.ShouldBindJSON(&batch)` (ignore bind errors — an unparseable body should still 200 per the 'never wedge the client' rule, matching applySessionUpdate's `_ = c.ShouldBindJSON(&req)` pattern at play.go:375); loop each session, resolve its item id via h.resolveBookID(user.ID, session.ItemID), skip on !ok, else build a progressPatchRequest from the session fields and call h.applyProgressUpdate(user.ID, bookID, req) — log-and-continue on a per-item apply error rather than aborting the batch, mirroring MediaProgressBatchUpdate's spirit but NOT its early-return-on-error at progress.go:178-181 (that early return is wrong for THIS endpoint per the spec's explicit 'a 4xx wedges the replay queue' warning — do not copy that part).
4. Register `r.POST("/api/session/local-all", auth, h.SessionLocalAll)` in internal/server/handlers/abs/handler.go's Register method, next to the new SessionLocal route from part 1.
5. Add the route to the documented list near internal/server/wire_abs_routes.go:636-637.
6. Bump version headers on all touched files.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_154.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A session with a currentTime but no duration should still apply — do not require duration; only reject nothing (per the 'always accept, always 200' spec rule the endpoint never rejects at all).
- An empty sessions array (either shape) should be a 200 no-op, not an error.
- A session id that resolves via resolveBookID to a book the current user does not otherwise have progress for should create a new progress row (same as any other first-time applyProgressUpdate call), not be treated specially.

## Tests

- {'file': 'internal/server/handlers/abs/play_test.go', 'name': 'TestSessionLocalAll_AcceptsBareArrayBody (new)', 'asserts': 'a bare JSON array body (abs-shim shape) is accepted and applies progress for each resolvable item'}
- {'file': 'internal/server/handlers/abs/play_test.go', 'name': 'TestSessionLocalAll_AcceptsObjectWithSessionsKey (new)', 'asserts': 'an {"sessions":[...],"deviceInfo":{...}} body (AudioBooth shape) is accepted and applies progress identically to the bare-array case'}
- {'file': 'internal/server/handlers/abs/play_test.go', 'name': 'TestSessionLocalAll_UnknownItemID_SkippedNot500 (new)', 'asserts': 'a session naming an item id that does not resolve is silently skipped and the endpoint still returns 200, not a partial failure'}
- {'file': 'internal/server/handlers/abs/play_test.go', 'name': 'TestSessionLocalAll_MalformedBody_StillReturns200 (anti-over-suppression, new)', 'asserts': "an unparseable/garbage body still returns 200 rather than 400, matching the spec's 'never wedge the offline replay queue' requirement — proves the handler does not accidentally reintroduce a 4xx path"}

Anti-over-suppression test: `TestSessionLocalAll_MalformedBody_StillReturns200` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/server/handlers/abs/... -run TestSessionLocalAll` passes for all four new tests.
- [ ] Neither new route ever returns a 4xx or 5xx for any request body shape, verified by the malformed-body test.
- [ ] Anti-over-suppression test: `TestSessionLocalAll_MalformedBody_StillReturns200` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_154.md`.

## Commit message

```
feat(server-handlers): Implement POST /api/session/local-all (batch local-session s (TODO L4507)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `Neither new route ever returns a 4xx or 5xx for any request body shape, verified by the malformed-body test.` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

The spec (docs/specs/2026-07-29-abs-sync-api-design.md) is unusually thorough for this API family — read §1.8 and §1.9 in full before implementing, not just the excerpts cited here, since it documents several client-specific quirks (timestamp tie-breaking, forward-only guards) that apply to any progress-writing endpoint including this one.
