<!-- file: docs/agent-tasks/todo-completion-2026-09/server-handlers/TASK-328-ipratelimiter-sweeps-the-entire-ip-map-under-one.md -->
<!-- version: 1.0.0 -->
<!-- guid: fa042a3a-41e1-406c-8cfa-3ec19a245590 -->
<!-- last-edited: 2026-09-10 -->

# TASK-328 — IPRateLimiter sweeps the entire IP map under one mutex on every request (SV-04)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `SV-04` (audit_server_handlers.json)

**Priority:** P3 · **Effort:** S · **Recommended subagent:** Haiku-class · server-handlers subagent · **Depends on:** none · **Wave:** 1 

Source: Wave 3 audit finding `SV-04` (audit_server_handlers.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/path/to/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-328" -b agent/server-handlers-328-ipratelimiter-sweeps-the-entire-ip-map-u origin/main
cd "$REPO/.worktrees/server-handlers-328"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Move the idle-eviction sweep off the request path (e.g. a periodic ticker goroutine that locks briefly and purges) so limiterForIP's per-request cost is O(1) map lookup, not O(n) sweep.

Why it matters: This is a security control (abuse mitigation) whose own implementation gets slower and more lock-contended as the number of distinct recent client IPs grows -- exactly the condition a real abuse burst (many IPs, or IPv6 rotation) produces. Every concurrent request serializes on one mutex while one goroutine walks the whole map, so enabling rate limiting under a distributed-IP attack can itself become the bottleneck instead of absorbing it.

## Background (verify before editing)

- limiterForIP (ratelimit.go:47-72) takes r.mu.Lock() (a plain sync.Mutex, not RWMutex) and then does `for key, entry := range r.entries { ... delete ... }` -- an O(n) full-map scan-and-delete of every tracked IP's idle entries -- on every single request that passes through the rate limiter, before even doing the per-IP lookup. The limiter is installed globally on the /api/v1 group when EnableRateLimit is on (server_lifecycle.go:1152-1157).
- Anchor: `internal/server/middleware/ratelimit.go:47` (audit `SV-04`, confidence medium, severity low).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -f internal/server/middleware/ratelimit.go   # the file the finding is anchored to still exists
  sed -n '41,53p' internal/server/middleware/ratelimit.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/server/middleware/ratelimit.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_server_handlers_328.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/middleware/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_server_handlers_328.md`.

## Commit message

```
fix(server-handlers): IPRateLimiter sweeps the entire IP map under one mutex on every reques (SV-04)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Pure code change: rollback = `git revert` the commit. If the re-verify greps show the fix already present, run acceptance instead of re-implementing.

## Coordinator notes

Standard lane: coordinator may admin-merge on a green gate.
