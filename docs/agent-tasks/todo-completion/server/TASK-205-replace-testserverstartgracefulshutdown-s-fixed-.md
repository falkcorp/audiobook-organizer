<!-- file: docs/agent-tasks/todo-completion/server/TASK-205-replace-testserverstartgracefulshutdown-s-fixed-.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0d3d5f7c-fb7f-4375-9ceb-d35b8ab3ae27 -->
<!-- last-edited: 2026-08-21 -->

# TASK-205 — Replace TestServerStartGracefulShutdown's fixed 6s sleep with a bounded readiness poll (TODO.md L283)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · server subagent · **Why:** requires touching Server struct + Start() + a test in the same package without breaking other tests that construct Server; needs the reader to understand goroutine/channel closing semantics correctly to avoid a new race · **Depends on:** TASK-204 · **Wave:** 5

Source: `TODO.md` line 283 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "The unconditional `time.Sleep(6 * time.Second)` be" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-18.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-205-replace-testserverstartgracefulshutdown-s-fixed-" -b agent/server-205-replace-testserverstartgracefulshutdown-s-fixed- origin/main
cd "$REPO/.worktrees/server-205-replace-testserverstartgracefulshutdown-s-fixed-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Remove the unconditional 6-second wall-clock cost from TestServerStartGracefulShutdown by replacing the fixed sleep with a deterministic readiness signal: add an unexported `shutdownArmed chan struct{}` field to the Server struct (internal/server/server.go, near the other lifecycle fields around line 282's bgCtx/bgCancel/bgWG block), initialize it with `make(chan struct{})` in NewServer, and `close(s.shutdownArmed)` immediately after the `signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)` call in Server.Start (internal/server/server_lifecycle.go:444). In the test, replace `time.Sleep(6 * time.Second)` with a select that waits on `server.shutdownArmed` (with a short bounded timeout, e.g. 5s, as a safety net in case Start never reaches that line) before sending the SIGTERM.

## Background (verify before editing)

- internal/server/server_lifecycle.go:444 registers signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM) inside Server.Start, after HTTP has already been started at line 415 (configureAndStartHTTP).
- internal/server/server_more_test.go:332-337 starts Server.Start in a goroutine, sleeps 6s (assumed enough time for Start to reach signal.Notify), then sends SIGTERM to its own PID.
- internal/server/server_more_test.go:309 shows the test's package is `server` (same as server.go/server_lifecycle.go), so a new unexported Server field needs no export or test-only accessor shim.
- internal/server/cache_warmers_bgwg_test.go already uses testify's assert.Eventually as the package's established polling idiom, so this fix does not introduce a new dependency.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "time.Sleep(6\|syscall.Kill" internal/server/server_more_test.go   # 2 hits, L336 and L337 — the sleep precedes the SIGTERM send in the same test
  grep -n "configureAndStartHTTP\|signal.Notify(quit" internal/server/server_lifecycle.go   # configureAndStartHTTP at L415, signal.Notify at L444 -- Notify happens after HTTP is already serving — signal.Notify is registered well after HTTP startup in Server.Start, creating the race the sleep guards against
  grep -n "^package server" internal/server/server.go internal/server/server_more_test.go   # both files declare package server — Server struct is defined in the same package as the test (package server), so a new unexported readiness field is directly visible to the test with no exported API change
  ```

### Reuse — don't invent

- Use `assert.Eventually (testify)` in `internal/server/cache_warmers_bgwg_test.go` (verify: `grep -n "assert.Eventually" internal/server/cache_warmers_bgwg_test.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/server/server.go, inside `type Server struct { ... }` (starts line 164), add a new field near the bgCtx/bgCancel/bgWG group (around line 282-284): `shutdownArmed chan struct{} // closed once Start has registered the OS signal handler; tests use this instead of a fixed sleep before sending a real signal`.
2. Find Server's constructor (`func NewServer(store database.Store) *Server` at internal/server/server.go:440) and initialize the new field: `shutdownArmed: make(chan struct{}),` in the struct literal, or `server.shutdownArmed = make(chan struct{})` right after `server` is constructed if it uses a multi-step build.
3. In internal/server/server_lifecycle.go, immediately after the `signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)` line (444), add `close(s.shutdownArmed)`.
4. In internal/server/server_more_test.go, replace the line `time.Sleep(6 * time.Second)` (336) with a bounded wait: `select { case <-server.shutdownArmed: case <-time.After(5 * time.Second): t.Fatal("server did not arm its shutdown signal handler in time") }` -- keep the existing comment block above it (it documents the *subsequent* 60s shutdown budget and remains accurate) but delete/replace only the sleep line itself.
5. Grep for any other test in the same package that also constructs a Server via a path that bypasses NewServer (e.g. a raw `&Server{}` literal) and confirm it does not panic on a nil shutdownArmed channel being read; if any such test calls Start() (not just unit-tests individual handlers), it will need the same lazy-init guard -- add `if s.shutdownArmed == nil { s.shutdownArmed = make(chan struct{}) }` at the top of Start() as a defensive fallback so a Server built without NewServer does not nil-deref.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_205.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Start() returning an error before reaching signal.Notify (e.g. opRegistrationGate fails, or container.Start fails): shutdownArmed is never closed, so the test's select falls through to the 5s timeout and fails loudly with a clear message instead of hanging silently -- this is the correct behavior, not a regression, since the old fixed-sleep test would also eventually time out via the existing 60s <-done budget.
- A Server constructed without NewServer (bypassing the field initializer) and then Start() called directly: guarded by the defensive `if s.shutdownArmed == nil` fallback in Start() so it self-heals rather than nil-channel-blocking forever.

## Tests

- internal/server/server_more_test.go TestServerStartGracefulShutdown -- asserts the server starts, is signaled, and shuts down within the existing 60s budget; verify it now completes in well under 6s of artificial wait (the real bound becomes however long Start takes to reach signal.Notify, typically milliseconds).
- Run the full package once to confirm no other test regresses: go test ./internal/server/... -count=1 -run TestServerStartGracefulShutdown -v, then the full package: go test ./internal/server/... -count=1

Anti-over-suppression test: `N/A -- this is a latency fix, not a filter/guard/skip addition` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] grep -n 'shutdownArmed' internal/server/server.go internal/server/server_lifecycle.go internal/server/server_more_test.go shows the field declared, initialized, closed, and consumed
- [ ] go test ./internal/server/... -run TestServerStartGracefulShutdown -count=1 -v passes and its wall-clock time (visible in `go test -v` per-test PASS line) is well under 6s instead of >6s
- [ ] go build ./... && go vet ./... exit 0
- [ ] Anti-over-suppression test: `N/A -- this is a latency fix, not a filter/guard/skip addition` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_205.md`.

## Commit message

```
fix(server): Replace TestServerStartGracefulShutdown's fixed 6s sleep wit (TODO L283)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Directly reduces internal/server package wall-clock time, relevant to todo_line 10104/10600-part-26 (TODO-SRVTIMEOUT, internal/server package running 434-480s against a 600s default timeout) -- this single test currently burns a guaranteed 6s of that budget for no correctness benefit. Should land in the same PR as todo_line 280's comment fix since both touch the same test function.
