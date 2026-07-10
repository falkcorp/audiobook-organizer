<!-- file: docs/agent-tasks/bug-techdebt/TASK-05-warmers-bgwg.md -->
<!-- version: 1.0.0 -->
<!-- guid: c5c39395-06d2-4f5f-85ee-c414080a1bdb -->
<!-- last-edited: 2026-07-10 -->

# TASK-05 — Enroll the four fire-and-forget cache warmers in bgWG (WARMERS-NOT-IN-BGWG, #1794)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · lifecycle-concurrency subagent · **Why:** goroutine-lifecycle change needing a `-race` test and a skip-path that must not over-suppress · **Depends on:** none

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY per item (worktree/PR/CI) EXCEPT REPO-SIZE-1 (#1650) which is STOP-FOR-HUMAN: a git-history rewrite is destructive and invalidates every clone/worktree — produce the migration plan (BFG/filter-repo vs LFS options, coordination checklist, backup strategy) as a TASK brief whose ONLY deliverable is the plan document, then STOP.
**File-ownership:** none within INIT-9 — the only edited product file is `internal/server/server_lifecycle.go`, touched by no other task (TASK-02's staticcheck findings do NOT include it; verified at planning time).

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/bug-techdebt-warmers-bgwg" -b agent/bug-techdebt-warmers-bgwg origin/main
cd "$REPO/.worktrees/bug-techdebt-warmers-bgwg"
git rebase origin/main
```

## Goal

Enroll the four untracked fire-and-forget cache warmers — `warmFacetsCache`,
`warmLibrarySizes`, `warmAuthorsCache`, `warmSeriesCache` — in the server's named
background WaitGroup (`s.bgWG`, the `namedWaitGroup` in `internal/server/bg_wg.go`),
with a `s.bgCtx.Err()` early-exit so a shutting-down server skips them, exactly like
the sibling `library-list-warmer` already enrolled a few lines above. REUSE
`s.bgWG.Add(name)`/`defer s.bgWG.Done(name)` — do not invent a new tracking mechanism
and do not change any warmer function's signature.

## Background (verify before editing)

- The warmers share the lifecycle gap that produced the PEBBLE-CLOSED panic family:
  goroutines outliving `Close()` and hitting a closed Pebble store. #1781 enrolled
  `library-list-warmer` + `apikey-expiry-sweep`; #1794 is the follow-up for these four.
- The comment block above `startCacheWarmers` currently says the four warmers "remain
  intentionally untracked ... Do not 'promote' the untracked ones here." **#1794
  reverses that decision** — you MUST rewrite that comment, or the file contradicts
  its own code.
- Warmer bodies live elsewhere and are NOT edited: `warmFacetsCache`
  (`internal/server/audiobooks_helpers.go`), `warmLibrarySizes`
  (`internal/server/server_helpers.go`), `warmAuthorsCache`/`warmSeriesCache`
  (`internal/server/entity_cache_warmers.go`). All changes go in the LAUNCH sites.
- `s.bgWG` methods take a string name; `Running()` returns the currently-registered
  names — use it in the test.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'go s.warmAuthorsCache\|go s.warmSeriesCache' internal/server/server_lifecycle.go
  # Expected: 2 hits (~:724, ~:725) — two of your four launch sites
  grep -n 'go s.warmFacetsCache\|go s.warmLibrarySizes' internal/server/server_lifecycle.go
  # Expected: 2 hits (~:704, ~:709) — the other two launch sites
  grep -n 'bgWG.Add("library-list-warmer")' internal/server/server_lifecycle.go
  # Expected: 1 hit (~:719) — the enrolled sibling pattern to copy
  grep -n 'remain intentionally' internal/server/server_lifecycle.go
  # Expected: 1 hit (~:699) — the stale comment you must rewrite
  grep -n 'func (n \*namedWaitGroup) Running' internal/server/bg_wg.go
  # Expected: 1 hit — the observability hook for your test
  ```

## Step-by-step

1. In `startCacheWarmers` (`internal/server/server_lifecycle.go`), convert each of the
   four launches from `go s.warmX()` to the enrolled shape, copying the
   `library-list-warmer` block found by the grep above:
   ```go
   s.bgWG.Add("facets-warmer")
   go func() {
       defer s.bgWG.Done("facets-warmer")
       if s.bgCtx.Err() != nil {
           return // server already shutting down — skip, never warm a closing store
       }
       s.warmFacetsCache()
   }()
   ```
   Names (use exactly): `facets-warmer`, `library-sizes-warmer`, `authors-warmer`,
   `series-warmer`. Keep each launch at its current position (facets/library-sizes
   before the library-list-warmer block, authors/series after), preserving existing
   inline comments about WHAT each warmer does.
2. Rewrite the stale doc comment (the "remain intentionally untracked ... Do not
   'promote'" block): state that all four are now bgWG-enrolled per #1794 (follow-up
   to #1781) so shutdown drains them before the store closes.
3. Edge-case semantics (spelled out so there is no guessing): the `bgCtx.Err()` check
   is a SKIP-on-shutdown, not a gate on startup — a live (non-canceled) context MUST
   still run every warmer. Do not add any other condition (no config flag, no timing).
4. Add `internal/server/cache_warmers_bgwg_test.go` (NEW; package `server`) with a
   task-unique helper name if you need one (parallel-test-helper-collision rule):
   - `TestStartCacheWarmers_SkipOnCanceledCtx` — build the minimal `Server` the way
     the existing tests in `internal/server/server_test.go` construct one (find the
     fixture: `grep -n 'func newTestServer\|func setupTestServer' internal/server/server_test.go`
     — Expected: ≥1 hit; mirror it), cancel its `bgCtx`, call `s.startCacheWarmers()`,
     then `s.bgWG.Wait()` must return promptly with no panic (warmers skipped on a
     store that may be closed).
   - `TestStartCacheWarmers_EnrolledInBgWG` (anti-over-suppression) — with a LIVE
     bgCtx and the test fixture's working store, call `s.startCacheWarmers()` and
     assert (a) `s.bgWG.Wait()` returns (all four completed and Done'd) and (b) at
     least one warmer observably executed (e.g. the facets/authors cache is non-empty
     afterwards, or use `Running()` sampled right after launch to observe the four
     names). The warmers must NOT be permanently skipped.
5. Bump file headers (version + last-edited) on both files; keep guids.
6. Update CHANGELOG.md (prepend) and check off the TODO.md item (locate:
   `grep -n 'WARMERS-NOT-IN-BGWG' TODO.md` — Expected: 1 hit ~:1263).
7. Run the gate (below).

## How to test

```bash
go test ./internal/server/ -run 'TestStartCacheWarmers' -race -v
# Expected: both new tests pass under -race
go test ./internal/server/ -short -race
# Expected: green — this package hosts the PEBBLE-CLOSED flake family; a regression here shows up as "pebble: closed" panics
make ci
```

staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files
you changed; the merge gate is Minimal CI green. The `sdkguard` step is ALSO red on
main (#1795, fixed by TASK-03) — a failure listing only `internal/logger` +
`internal/dedup/unified` is pre-existing, not yours.

## Acceptance criteria

- [ ] `grep -c 'bgWG.Add("facets-warmer")\|bgWG.Add("library-sizes-warmer")\|bgWG.Add("authors-warmer")\|bgWG.Add("series-warmer")' internal/server/server_lifecycle.go` returns 4
- [ ] `grep -n 'remain intentionally untracked' internal/server/server_lifecycle.go` returns 0 hits (stale comment rewritten)
- [ ] `TestStartCacheWarmers_SkipOnCanceledCtx` green under `-race` (canceled ctx → skip, Wait returns, no panic)
- [ ] `TestStartCacheWarmers_EnrolledInBgWG` green under `-race` — live ctx still runs the warmers (anti-over-suppression)
- [ ] No warmer function signature changed (`git diff origin/main -- internal/server/entity_cache_warmers.go internal/server/audiobooks_helpers.go internal/server/server_helpers.go` is empty)
- [ ] Tests green; vet/lint clean (scoped staticcheck on the two changed files).
- [ ] File headers bumped on every changed file.

## Commit message

```
fix(server): enroll all four cache warmers in bgWG with shutdown skip (#1794)

warmFacetsCache/warmLibrarySizes/warmAuthorsCache/warmSeriesCache were
fire-and-forget and could outlive Close() (PEBBLE-CLOSED family lifecycle gap;
follow-up to #1781). Each launch now registers a named bgWG entry and skips on
an already-canceled bgCtx, mirroring the enrolled library-list-warmer sibling.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/bug-techdebt-warmers-bgwg
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n 'bgWG.Add("authors-warmer")' internal/server/server_lifecycle.go` hits,
this task is already applied — run the acceptance checks instead of re-applying.
Rollback = revert the single commit; the warmers return to fire-and-forget launches
(pre-#1794 behavior) and no data, cache format, or API surface is touched.
