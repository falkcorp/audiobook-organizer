<!-- file: docs/agent-tasks/todo-completion/server/TASK-206-split-or-speed-up-the-internal-server-test-packa.md -->
<!-- version: 1.1.0 -->
<!-- guid: 791c34cb-1733-43c6-8202-c450c775b5bb -->
<!-- last-edited: 2026-09-02 -->

# TASK-206 — Split or speed up the internal/server test package -- migrate call sites to a lighter newTestServer helper (TODO-SRVTIMEOUT)

> **Status 2026-09-02:** 🟡 OPEN — still worth doing — grep 'func newTestServer' internal/server/*.go = 0 hits; the 4 heavy fixtures unchanged (server_test.go:50,57,61,153); package grew 139 -> 164 *_test.go files. Recommendation: keep - no lighter fixture exists; scope grew.

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Opus-class · server subagent · **Why:** requires profiling which parts of full server construction (container.Start, search index warmup, cache warmers, backfill resume scans) are actually needed by each of ~60 call sites, then building a genuinely lighter constructor without silently changing what each migrated test exercises -- easy to get subtly wrong at this scale · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 10104 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**TODO-SRVTIMEOUT**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-18.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-206-split-or-speed-up-the-internal-server-test-packa" -b agent/server-206-split-or-speed-up-the-internal-server-test-packa origin/main
cd "$REPO/.worktrees/server-206-split-or-speed-up-the-internal-server-test-packa"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Build a new, lighter-weight newTestServer(t *testing.T) (*Server, func()) helper in internal/server/server_test.go that skips whatever parts of full Server construction (container.Start's service dependency graph, search-index warmup, cache warmers, backfill/interrupted-op resume scans) a given HTTP-handler-focused test does not actually exercise, then migrate the ~60 call sites currently paying the full setupTestServer cost unnecessarily over to it -- reducing internal/server's total package test time from its current 434-480s toward something with real headroom under Go's 600s default per-package timeout, without changing what any migrated test actually asserts.

## Background (verify before editing)

- The TODO's own diagnosis: this package runs 434-480s against a 600s default timeout (under 30% headroom), and any concurrent machine load can tip a genuinely-slow-but-correct run into looking like a deadlock -- a 2026-07-31 investigation on PR #2083 went down exactly that false trail, naming operations/registry.(*Registry).Shutdown blocked on sync.WaitGroup.Wait at registry.go:1030 as the culprit, when the same commit passed cleanly in 480s with no competing load.
- internal/server/server_test.go already has FOUR setup-helper variants (setupTestServer, setupTestServerRealFS, setupTestServerFS, setupTestServerWithStore) -- all apparently doing full-weight construction with different storage backends, none doing a stripped-down construction for tests that only need routing/handler logic exercised.
- 139 _test.go files (35,610 total lines) live directly under internal/server/ -- the ~60 call sites the owner decision references are almost certainly a subset of these calling one of the four existing setup helpers where a lighter one would suffice.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rn 'func newTestServer' internal/server/*.go   # 0 hits — no newTestServer helper exists yet -- it needs to be created, not just called differently
  grep -n 'func setupTestServer' internal/server/server_test.go   # 4 hits: setupTestServer L48, setupTestServerRealFS L55, setupTestServerFS L59, setupTestServerWithStore L151 — the existing heavy setup-helper family that most tests currently call
  ls internal/server/*_test.go | wc -l   # 139 files — the package is large enough to plausibly be the timeout's root cause (many tests, most likely all paying the full server-construction cost)
  grep -n 'func (r \*Registry) Shutdown' internal/operations/registry/registry.go   # 1 hit, L997 (close to the L1030 WaitGroup.Wait the TODO cites) — the specific goroutine the false-deadlock trail named, which the TODO says was misdiagnosed as a hang rather than a slow-but-correct run
  ```

### Reuse — don't invent

- Use `setupTestServer / setupTestServerFS / setupTestServerWithStore (the existing heavy helpers most call sites should be migrated AWAY from, toward a new lighter one)` in `internal/server/server_test.go` (verify: `grep -n 'func setupTestServer' internal/server/server_test.go`) — do NOT write a parallel helper.

## Step-by-step

1. Read setupTestServer (server_test.go:48) in full to see exactly what it constructs -- likely calls NewServer then Start() or a partial-Start equivalent, wiring the full container/service graph.
2. Instrument (temporarily, for profiling only -- not a permanent change) setupTestServer with time.Now()/time.Since() bracketing around its major phases (container.Start, search index open, cache warmers, backfill resume) and run `go test ./internal/server/... -count=1 -v 2>&1 | ...` capturing per-test timing, OR use `go test ./internal/server/... -count=1 -cpuprofile` / the existing `go test -v` PASS-line timings to identify the handful of setup phases consuming the bulk of the 434-480s across all 139 files.
3. Design newTestServer(t) as a constructor that builds a Server with only the pieces a plain HTTP-handler test needs (router wired, store present, auth/permission middleware present) and explicitly SKIPS the expensive phases identified above (search index warmup, cache warmers, backfill resume scans) unless a test explicitly opts in via a variant (e.g. keep setupTestServer for the ~tests that specifically assert on search/cache/backfill behavior, and only migrate the ones that don't touch those subsystems at all).
4. Grep every call site of setupTestServer/setupTestServerFS/setupTestServerWithStore across internal/server/*_test.go, and for each one, check whether the test body ever references s.searchIndex, s.dedupCache/listCache/facetsCache/authorsCache/seriesCache, s.scheduler, or anything backfill-related -- if not, it is a newTestServer migration candidate.
5. Migrate candidates in batches (not all ~60 at once), running `go test ./internal/server/... -count=1` after each batch to confirm no regression and to measure the cumulative time reduction.
6. Once migration is complete, re-measure the full package's wall-clock time and compare against the 434-480s baseline; if still tight against 600s, consider the TODO's alternative (an explicit longer -timeout in the Makefile test targets) as a belt-and-suspenders addition, not a replacement for the newTestServer work.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_206.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A test migrated to newTestServer that turns out to implicitly depend on a skipped subsystem (e.g. asserts on a cache being warm, or on search results, without obviously referencing the relevant field) would fail loudly with a nil-pointer or empty-result assertion -- treat any such failure as a signal that test belongs back on setupTestServer, not as a bug in newTestServer to work around.
- Do not silently drop test coverage by migrating a test whose assertions actually DO depend on full-server behavior just to hit a target call-site count -- the ~60 figure is an estimate from the owner decision, not a quota to force.

## Tests

- go test ./internal/server/... -count=1 -v (full package) both before and after migration, comparing wall-clock time via the `go test -v` per-test PASS lines and the final `ok ... Xs` summary line.
- Spot-check a handful of migrated tests individually (go test ./internal/server/... -run <TestName> -count=1 -v) to confirm their assertions still pass unchanged after switching from setupTestServer to newTestServer.

Anti-over-suppression test: `N/A -- this is a test-infrastructure performance fix, not a filter/guard/skip on application logic` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] grep -c 'newTestServer(' internal/server/*_test.go sums to roughly 60 across the package (the owner-decision's stated target)
- [ ] go test ./internal/server/... -count=1 completes with materially more than 30% headroom under Go's 600s default timeout (i.e. well under ~420s, ideally much less)
- [ ] go build ./... && go vet ./... exit 0
- [ ] Anti-over-suppression test: `N/A -- this is a test-infrastructure performance fix, not a filter/guard/skip on application logic` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_206.md`.

## Commit message

```
refactor(server): Split or speed up the internal/server test package -- migrat (TODO-SRVTIMEOUT)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Duplicate/companion of todo_line 10600 part 1 (same TODO-SRVTIMEOUT item, referenced a second time as a one-line summary elsewhere in TODO.md) -- implement once, this entry carries the full detail. Also complements todo_line 283 (removes a guaranteed 6s sleep from one test in this same package) -- both reduce the same package's wall-clock budget and can land as separate PRs without conflicting.
