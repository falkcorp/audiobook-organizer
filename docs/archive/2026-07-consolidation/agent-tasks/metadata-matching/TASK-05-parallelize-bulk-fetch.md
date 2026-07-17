<!-- file: docs/agent-tasks/metadata-matching/TASK-05-parallelize-bulk-fetch.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5237876f-b93c-4772-87c3-287e78641c49 -->
<!-- last-edited: 2026-07-10 -->

# TASK-05 — Parallelize the bulk metadata fetch within provider rate limits (INIT-3-T3) ⚠ review-critical

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). Config extraction (T1) MUST default to today's literal values — zero behavior change until an operator tunes them.
**File-ownership:** `internal/server/metadata_ops.go` — before dispatch, confirm no concurrent INIT-9 / INIT-10 wave has an open worktree touching this file (`git worktree list` + check sibling plans). No collision inside this workstream.

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Sonnet-class ⚠ coordinator line-review · concurrency subagent · **Why:** rewrites a production bulk-op loop for concurrency; races and resume regressions are high-stakes and partially invisible to CI · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/metadata-matching-parallelize-bulk-fetch" -b agent/metadata-matching-parallelize-bulk-fetch origin/main
cd "$REPO/.worktrees/metadata-matching-parallelize-bulk-fetch"
git rebase origin/main
```

## Goal

Replace the serial per-book loops in `runBulkMetadataFetchForBookIDs` and
`runBulkMetadataFetchAll` (`internal/server/metadata_ops.go`) with a bounded worker pool
(default **4 workers** — network-bound, deliberately smaller than NumCPU per the CLAUDE.md
concurrency mandate) plus per-provider concurrency caps (**fixed constant: 2 in-flight calls per
source** — not config), while preserving the op contract exactly: op IDs, params, resume-via-OperationResult
semantics, progress cadence, and the per-book sequential priority-ordered source chain. Use
`errgroup.Group` + `SetLimit` — the CLAUDE.md-sanctioned bounded pool — and do NOT invent a new
pool abstraction.

## Background (verify before editing)

- The serial hot loop: `for i, w := range work` inside `runBulkMetadataFetchForBookIDs`
  (~line 532), with a nested sequential `for _, src := range sourceChain` (~562). The whole-library
  twin is `runBulkMetadataFetchAll` (~55). Op registration `RegisterBulkMetadataFetchOp` (~379,
  re-locate with the anchor grep below) must not change.
- **Parallelize the OUTER book loop ONLY** (spec Decision 4). The per-book source chain stays
  sequential: it is priority-ordered with early-exit on first success, and each source is wrapped
  in a `ProtectedSource` circuit breaker (threshold 5, 30s cooldown —
  `internal/metadata/circuitbreaker.go`); Hardcover additionally has its own 60-rpm limiter
  (`waitForRateLimit` in `internal/metadata/hardcover.go`). Fanning out the chain would multiply
  provider load ~6× and break early-exit semantics.
- Per-provider cap: N pool workers could still stampede ONE provider (all books hitting Audible
  first). Add a per-source-name `chan struct{}` semaphore map (size = per-provider cap) acquired
  around each `src.SearchByTitleAndAuthor` / `src.SearchByTitle` call.
- Concurrency knobs: the per-provider cap is a FIXED internal constant 2 — deliberately NOT
  config (reviewed; the `ProtectedSource` breaker + provider limiters sit beneath it and no
  per-deployment need exists). Workers: read
  `config.AppConfig.MetadataScoring.BulkFetchWorkers` IF TASK-02 has merged (grep below);
  otherwise use a local constant 4 with a `// TODO(INIT-3-T1): move to MetadataScoringConfig`
  marker — do not block on TASK-02. **Wave-1 tunability disclosure (reviewed):** until TASK-02/03
  land, the fan-out is NOT runtime-throttleable — the sole mitigation for a stampede is
  revert-PR. This is accepted because the per-provider semaphore, circuit breaker, and
  Hardcover's 60-rpm limiter bound provider impact; keep the constants trivially greppable so
  TASK-02 promotes the worker count cleanly.
- Shared state that becomes racy: `found`, `notFound` (make them atomics — `completed` already
  is), the `done` resume map (build BEFORE dispatch, treat as read-only inside workers), the
  cache-read+`CreateOperationResult` writes (store calls are safe; never share loop variables).
- Pool choice (resolved by review — one instruction, not two): use `errgroup.Group` +
  `SetLimit(workers)`. Do NOT use `registry.RunItems` here — this loop's
  `OperationResult`-resume rows and custom every-50 progress cadence don't fit its reporter
  contract. `internal/plugins/acoustid/backfill.go:118` remains the conceptual parallel-sibling
  reference only (CLAUDE.md names errgroup+SetLimit as its sanctioned equivalent). Never
  hand-roll goroutine bookkeeping.
- Edge semantics: ctx cancellation must stop dispatching promptly and return `ctx.Err()`; a
  single book's fetch error must NOT fail the whole op (today it records `not_found`/error result
  rows and continues — preserve that).

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'func (s \*Server) runBulkMetadataFetchForBookIDs' internal/server/metadata_ops.go   # ~439
  grep -n 'func (s \*Server) runBulkMetadataFetchAll' internal/server/metadata_ops.go          # ~55
  grep -n 'func (s \*Server) RegisterBulkMetadataFetchOp' internal/server/metadata_ops.go      # ~379, op registration — must NOT change
  grep -n 'for i, w := range work' internal/server/metadata_ops.go                             # the serial loop(s)
  grep -n 'for _, src := range sourceChain' internal/server/metadata_ops.go                    # per-book chain (keep sequential)
  grep -n 'registry.RunItems' internal/plugins/acoustid/backfill.go                            # conceptual sibling only (errgroup is the pool here), ≥1 hit
  grep -n 'func NewProtectedSource' internal/metadata/circuitbreaker.go                        # breaker stays beneath
  grep -n 'func (c \*HardcoverClient) waitForRateLimit' internal/metadata/hardcover.go         # provider limiter stays beneath
  ```
  Zero hits on any of these = STOP and report drift.

## Step-by-step

1. Refactor the per-book body of each serial loop into a `processOne(ctx, w) error`-shaped
   closure/method, keeping its logic byte-for-byte where possible (fragment skip, cache check,
   source chain, result row, counters via atomics).
2. Drive it with `errgroup.Group` + `g.SetLimit(workers)` over `work`, workers default 4.
   Progress cadence: keep "every 50 / final" using the atomic counter, same message format.
3. Add the per-source semaphore map keyed by `src.Name()` (fixed constant cap 2) around the two
   search calls only — NOT around cache reads or result writes.
4. Convert `found`/`notFound` to `atomic.Int64`; verify the `done` resume map is fully built
   before `g.Go` dispatch and never written afterwards.
5. Edge semantics (state them in code comments AND tests): ctx cancel → stop dispatch, return
   `ctx.Err()`; per-book error → record result row, continue (op does not fail); zero books →
   clean completion with 0/0 progress.
6. Apply the same pool to `runBulkMetadataFetchAll`'s equivalent loop (re-grep for its loop shape;
   it shares resume semantics).
7. Tests (`internal/server/metadata_ops_test.go`, create if absent) with fake sources: worker cap
   respected (max in-flight ≤ 4), per-provider cap respected (max in-flight per source ≤ 2),
   resume-skip still exact, counters exact under concurrency, ctx cancel stops promptly. Run with
   `-race`.
8. Purely a loop-strategy transform: no signature, param, op-ID, or result-row schema changes.
9. Anti-over-suppression: N/A (no filter/guard/veto/skip/dedupe path is added; the existing
   fragment-skip behavior is preserved unchanged).
10. Bump headers on every touched file; keep existing guids.

## How to test

```bash
make ci
# caveat: staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files
# you changed; the merge gate is Minimal CI green.
go test ./internal/server/ -run 'BulkMetadataFetch|BulkFetch' -race -v
```

## Acceptance criteria

- [ ] `grep -n "SetLimit" internal/server/metadata_ops.go` hits (bounded errgroup pool present)
- [ ] `grep -n 'for i, w := range work' internal/server/metadata_ops.go` returns 0 hits (serial loop gone)
- [ ] Per-provider semaphore exists and is tested (max in-flight per source assertion)
- [ ] Resume, counter-exactness, and ctx-cancel tests green under `-race`
- [ ] Op registration untouched: `git diff origin/main -- internal/server/metadata_ops.go | grep -c "def_id\|RegisterBulkMetadataFetchOp"` shows no contract changes
- [ ] Anti-over-suppression: N/A
- [ ] Tests green; vet/lint clean (`make ci`, staticcheck scoped to changed files).
- [ ] File headers bumped on every changed file.

## Commit message

```
perf(server): parallelize bulk metadata fetch with provider-respecting bounds (INIT-3-T3)

The whole-library bulk fetch ran a serial for-range with a nested
sequential provider chain (the exact single-core shape behind the
2026-07-05 dedup incident). Outer loop now runs on a bounded pool
(4 workers, network-bound) with per-provider semaphores (2 in-flight per
source); the per-book chain stays sequential to preserve priority
early-exit, circuit breakers, and provider rate limits. Resume rows,
op contract, and progress cadence unchanged; counters are atomics.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR — NO SELF-MERGE (⚠ review-critical)

```bash
git push -u origin agent/metadata-matching-parallelize-bulk-fetch
gh pr create --fill
# STOP HERE. Do NOT run `gh pr merge`.
```

**This task has NO merge command in ANY run mode (reviewed — a review gate that exists in only
one run mode is not a gate).** In standalone mode: push, open the PR, post the acceptance
checklist + COMPLETED/REMAINING/BLOCKED counts, and STOP — coordinator/human line-by-line review
of the diff is a hard precondition for merge (races and resume regressions are partially
invisible to CI). Under a coordinated sweep, STOP after commit — the coordinator owns
push/PR/merge and performs the same line review before merging.

## Idempotency / Rollback

If `grep -n "SetLimit" internal/server/metadata_ops.go` hits AND
`grep -n 'for i, w := range work' internal/server/metadata_ops.go` returns 0, the transform is
already done — run the acceptance checks instead of re-applying. Rollback = revert the commit; the
op contract (IDs, params, OperationResult resume rows) is identical either way, so in-flight or
resumed operations are unaffected by a revert.
