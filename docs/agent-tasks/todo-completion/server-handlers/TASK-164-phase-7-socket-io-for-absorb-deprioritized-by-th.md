<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-164-phase-7-socket-io-for-absorb-deprioritized-by-th.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0e1da0ea-1afe-4f3f-b839-75c72232f5f4 -->
<!-- last-edited: 2026-08-21 -->

# TASK-164 — Phase 7 — socket.io for Absorb (deprioritized by the item's own text; still unbuilt) (ABS-SYNC-Phase7)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · server-handlers subagent · **Why:** New protocol surface (socket.io) with a narrow scope (Absorb-only, one auth handshake), but nontrivial because it needs an actual socket.io-compatible server, not just a REST handler · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 10354 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**ABS-SYNC: Phase 7 — socket.io (Absorb only).** A" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-13.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-164-phase-7-socket-io-for-absorb-deprioritized-by-th" -b agent/server-handlers-164-phase-7-socket-io-for-absorb-deprioritized-by-th origin/main
cd "$REPO/.worktrees/server-handlers-164-phase-7-socket-io-for-absorb-deprioritized-by-th"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Implement a minimal socket.io endpoint sufficient to stop Absorb going offline after 5 failed reconnects: accept the socket.io handshake, respond to `emit('auth', <raw token string>)` by validating the token through the existing ABSIdentityResolver, and keep the connection alive. AudioBooth needs none of this (verified against its Package.swift per the item) — scope strictly to Absorb compatibility.

## Background (verify before editing)

- AudioBooth (the primary client, per the item) ships without any websocket dependency; only Absorb requires this, and only to avoid its own reconnect-storm/offline behavior — this is a compatibility shim, not a feature AO needs for its own sake.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rn 'socket.io\|socketio' internal/server/handlers/abs/*.go   # 0 hits — no socket.io implementation exists in the ABS handler package
  grep -n 'socket.io' internal/server/spa_fallback.go   # ≥2 hits (nonSPAPrefixes/nonSPAExact per abs-implementation-status.md's N-1 finding) — socket.io IS already excluded from the SPA-fallback 200-HTML bug for the AudioBooth/Absorb clients
  ```

### Reuse — don't invent

- Use `absGroup registration pattern to add a new route group` in `internal/server/wire_abs_routes.go` (verify: `grep -n 'absGroup := s.router.Group' internal/server/wire_abs_routes.go`) — do NOT write a parallel helper.

## Step-by-step

1. Confirm no existing Go socket.io library is already vendored (`grep -n 'socket.io\|googollee' go.mod go.sum`) before choosing a dependency; TASK-11's owner-decision note in this same scope ('Only this task may touch go.mod') suggests go.mod changes are gated — check whether TASK-11 has already landed and closed that gate, or whether adding a socket.io dependency needs the same sign-off.
2. Add a minimal socket.io v2/v3-compatible handshake handler (either via a vendored library or a hand-rolled minimal implementation, given the tiny surface needed: handshake + one 'auth' event) registered on the ABS router group in internal/server/wire_abs_routes.go.
3. On receiving `emit('auth', <token>)`, validate the token via the existing internal/server/middleware/absauth.go ABSIdentityResolver.ResolveBearer (or equivalent) and keep the socket open on success; close it (or respond with an auth-failure event) on failure — do not silently accept unauthenticated sockets.
4. Bump version headers on new/touched files.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_164.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A socket that never sends the auth event within a reasonable timeout should be closed, not left open indefinitely (resource leak).

## Tests

- internal/server/handlers/abs/socketio_test.go: TestSocketIOHandshake_AcceptsValidToken and TestSocketIOHandshake_RejectsInvalidToken — simulate the handshake + auth emit with a valid/invalid bearer token and assert the connection is kept/closed accordingly.

Anti-over-suppression test: `N/A — this is new functionality, not a filter` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `go test ./internal/server/handlers/abs/... -run SocketIO` passes
- [ ] Anti-over-suppression test: `N/A — this is new functionality, not a filter` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_164.md`.

## Commit message

```
feat(server-handlers): Phase 7 — socket.io for Absorb (deprioritized by the item's  (ABS-SYNC-Phase7)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (``go test ./internal/server/handlers/abs/... -run SocketIO` passes`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Item's own text explicitly deprioritizes this ('the primary client ships without it') — recommend the owner confirm it's still worth building before spending an M-effort task on it, rather than this scout assuming 'parked' on their behalf.
