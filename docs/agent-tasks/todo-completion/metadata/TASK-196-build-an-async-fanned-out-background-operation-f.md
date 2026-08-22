<!-- file: docs/agent-tasks/todo-completion/metadata/TASK-196-build-an-async-fanned-out-background-operation-f.md -->
<!-- version: 1.0.0 -->
<!-- guid: b4f753d1-af97-409d-b5a5-8a8263895acc -->
<!-- last-edited: 2026-08-21 -->

# TASK-196 — Build an async, fanned-out background operation for metadata matching -- the bulk dialog is a human-driven one-book-at-a-time loop today (TODO.md L4081)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Opus-class · metadata subagent · **Why:** new background operation touching an operations-registry op definition, a worker pool with per-request jitter/stagger against external metadata providers with their own rate limits, and a still-usable interactive UI path -- genuine design surface, not a mechanical change · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4081 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**CORRECTION to `20260814-matcher-writeback-backgr" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-18.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/metadata-196-build-an-async-fanned-out-background-operation-f" -b agent/metadata-196-build-an-async-fanned-out-background-operation-f origin/main
cd "$REPO/.worktrees/metadata-196-build-an-async-fanned-out-background-operation-f"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Build a new background operation (registered in the operations registry, alongside the existing maintenance/dedup ops in internal/server/metadata_ops.go) that fans a metadata SEARCH out across a filtered set of books using registry.RunItems' bounded worker pool, with a staggered start delay/jitter per request so concurrent workers do not flood the metadata providers all at once (the chain's per-source rate limiter and circuit breaker are already documented safe for this -- see waitForLimiter in service_search.go -- but a worker pool without added jitter can still open N simultaneous connections to a single-token-bucket source in the same instant, defeating a rate limiter designed for smoothed request pacing). This makes bulk metadata matching an ops-system-visible background job an operator can kick off and walk away from, instead of requiring them to click through BulkMetadataSearchDialog.tsx one book at a time -- the existing interactive dialog should remain available for single-book/manual review use, not be removed.

## Background (verify before editing)

- CORRECTION (owner, 2026-08-14) to an earlier design note (20260814-matcher-writeback-background-job.md): the write side of the matcher is already backgrounded; the actual blocking half is the metadata FETCH (search), and it is 'effectively a singleton' in the sense that the only bulk-search UI path forces one book through at a time via human clicks, not because of any code-level lock.
- metafetch.chainMu was the first suspect and is now ruled out by direct code reading: it guards only cached-chain CONSTRUCTION (an in-memory map lookup plus, on a cache miss, building client objects), and is released before any network call happens -- the chain itself is documented safe for concurrent worker pools since each source client carries its own rate limiter and circuit breaker.
- BulkMetadataSearchDialog.tsx confirmed as the actual bottleneck: filteredBooks[currentIndex] plus a per-current-book doSearch call is a textbook one-at-a-time interactive loop -- there is no server-side batching of this at all today.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  sed -n '91,102p' internal/metafetch/service_search.go   # shows mfs.chainMu.Lock()/defer Unlock() wrapping only a map-cache check and (on miss) buildSourceChainFromConfig, which builds client objects rather than making network calls itself — chainMu is scoped tightly around a cache check only, not held across any network call -- ruling it out as the singleton
  grep -n 'currentIndex\|filteredBooks\[currentIndex\]\|api.searchMetadataForBook' web/src/components/audiobooks/BulkMetadataSearchDialog.tsx   # confirms currentBook = filteredBooks[currentIndex] (L159) and a single per-current-book api.searchMetadataForBook call inside doSearch (L174), with currentIndex advanced by explicit UI interaction, not a loop over all books — the bulk metadata dialog is a client-side one-book-at-a-time interactive loop, not a background job
  grep -n 'func (mfs \*Service) searchMetadataForBook' internal/metafetch/service_search.go   # 1 hit, L243, a synchronous per-book function with no batching/fan-out of its own — the actual per-book search implementation this dialog calls into
  ```

### Reuse — don't invent

- Use `registry.RunItems (bounded worker pool + resume, the standing pattern for any whole-library-scale async op per CLAUDE.md)` in `internal/operations/registry/run_items.go` (verify: `grep -n 'func RunItems' internal/operations/registry/run_items.go`) — do NOT write a parallel helper.
- Use `an existing rate-limited per-source fan-out precedent already used for the interactive single-book path (per-source rate limiter + circuit breaker, cited by the TODO as already safe for concurrent worker pools)` in `internal/metafetch/service_search.go` (verify: `grep -n 'waitForLimiter\|rate.Limiter' internal/metafetch/service_search.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/server/metadata_ops.go, following the pattern of the plugin/maintenance ops already registered there (grep the file for an existing sdk.OperationDef-returning function as a template), define a new op (e.g. metadata.bulk-search-fetch) whose Params include a filter for which books to search (reuse whatever book-selection filter shape other bulk ops in this file already accept) and a dry_run/apply-style gate matching the report-then-confirm convention used elsewhere in this file if the op ends up writing anything (a pure SEARCH-and-cache op may not need an apply gate at all if it only populates a review queue rather than mutating book records directly -- confirm which by checking what BulkMetadataSearchDialog's apply flow actually persists today).
2. Implement the op body using registry.RunItems over the filtered book set, calling mfs.searchMetadataForBook (or SearchMetadataForBookWithOptions) per item inside the RunItems fn closure -- reuse the existing per-source rate limiter and circuit breaker in the chain (BuildSourceChain), do not build a second rate-limiting layer that could conflict with it.
3. Add explicit per-item jitter: before each item's search call, sleep a small randomized delay (e.g. 0-2s, tunable) so registry.RunItems' concurrent workers do not all fire their first request in the same instant -- this is the 'staggered start delays/jitter' the owner's correction specifically calls for, distinct from the existing per-source token-bucket limiter which paces steady-state throughput but does not prevent a burst on worker-pool startup.
4. Store each book's search RESULTS somewhere an operator can review afterward (likely the same place/shape BulkMetadataSearchDialog already expects, so the interactive dialog and the new background op produce compatible data -- check MetadataCandidate's existing persistence, if any, or whether results need a new lightweight cache/table).
5. Register the op with the operations registry (follow the exact registration call other ops in this file use) and confirm it appears in the ops UI/API the same way other maintenance/dedup ops do.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_metadata_196.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A metadata provider circuit breaker trips mid-run: the op must record which books failed due to the breaker (distinctly from a genuine no-match) so a resume/retry can target just those, not silently re-run the whole batch.
- The existing interactive BulkMetadataSearchDialog.tsx flow must keep working unmodified for operators who want manual per-book review -- this item ADDS a background alternative, it does not replace or remove the interactive path.
- Per CLAUDE.md's concurrency mandate, the worker pool must be BOUNDED (registry.RunItems already provides this via its Concurrency option) -- do not fan out unbounded goroutines even though this is a network-bound (not CPU-bound) workload; size it to respect the target providers' own rate limits, likely smaller than runtime.NumCPU().

## Tests

- internal/server/metadata_ops_bulk_search_test.go (new file): TestBulkSearchFetch_FansOutAcrossBooks -- seed a handful of books, run the op with a mock/fake metadata source, assert every book's search results are captured and the op reports progress via the standard registry.RunItems reporter.
- TestBulkSearchFetch_JitterDoesNotBlockCompletion -- assert the op still completes within a reasonable bound with jitter enabled (not an infinite/runaway delay), and that jitter is actually applied (e.g. by asserting request start timestamps are not all identical for a batch run concurrently).
- TestBulkSearchFetch_RespectsExistingRateLimiter -- confirm the op does not bypass the per-source limiter/circuit breaker already wired into BuildSourceChain (e.g. assert a tripped circuit breaker still short-circuits requests inside the new op the same way it does for the interactive single-book path).

Anti-over-suppression test: `N/A -- this is a new capability, not a filter/guard/skip` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/metafetch/... ./internal/server/... -count=1 && npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/server/... -run TestBulkSearchFetch -count=1 -v passes
- [ ] the new op appears in the operations registry listing and can be started/monitored through the same UI/API surface as other maintenance/dedup ops
- [ ] go build ./... && go vet ./... exit 0
- [ ] Anti-over-suppression test: `N/A -- this is a new capability, not a filter/guard/skip` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/metafetch/... ./internal/server/... -count=1 && npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_metadata_196.md`.

## Commit message

```
feat(metadata): Build an async, fanned-out background operation for metadata (TODO L4081)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go test ./internal/server/... -run TestBulkSearchFetch -count=1 -v passes`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This is new functionality, not a bug fix -- the TODO frames it as a correction to a prior (wrong) diagnosis that the write side was the bottleneck. review_critical=false since it only performs metadata SEARCH (read-only against providers) and populates review data, not a direct book-record mutation -- but if the implementation ends up auto-applying results without a human review step, re-flag as review_critical=true.
