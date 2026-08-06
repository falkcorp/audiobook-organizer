<!-- file: docs/plans/2026-08-05-metadata-results-cold-start-plan.md -->
<!-- version: 2.0.0 -->
<!-- guid: e4638536-a13f-431b-84f1-3120fb4f4995 -->
<!-- last-edited: 2026-08-05 -->

> ⚠️ **UNVERIFIED DRAFT — symbols not grep-checked.** Authored by an agent on
> 2026-08-05; the adversarial verification pass did not run (the workflow was
> halted by API rate limiting). Treat every code citation as a claim, not a fact.
> The design reasoning and measured production numbers are sound; the code
> references need checking before execution.


# Plan — metadata-results cold start

Implements [`docs/specs/2026-08-05-metadata-results-cold-start-design.md`](../specs/2026-08-05-metadata-results-cold-start-design.md)
(owner item 6). Four steps, each independently committable and independently
revertable.

**Branch:** `fix/metadata-results-cold-start` (worktree only — never main).
**Nothing in this plan writes to the library.** No `UpdateBook`, no `UpdateBookFile`,
no filesystem access, no `books/itunes/**`. Every change is to in-process cache
plumbing and one additive JSON field.

---

## Step 1 — Stale-while-revalidate + boot warmer (D1 + D2)

One commit. The warmer alone is not shippable value (a 60 s TTL means a boot warm
covers 60 seconds), so it lands with SWR.

### Files

| File | Intent |
|---|---|
| `internal/server/metadata_results_cache.go` | **Modify.** Add `metadataResultsStaleMax = 15 * time.Minute`. Add `inflight bool` to the `metadataResultsCache` struct (`:38`). Rewrite `latestMetadataResultsByBookCached` (`:55`) into the three-state fresh / stale / cold form from spec D2, returning an extra `age time.Duration`. Add `refreshMetadataResultsCacheAsync(store)` — one goroutine, guarded by `inflight`, `defer warmerRecover("metadata-results-refresh")`. Add `(*Server).warmMetadataResultsCache()` calling the cached getter and logging. Add a `trigger` field (`boot` \| `demand` \| `refresh`) to the existing `slog.Info("metadata-results cache rebuilt", ...)` at `:71-72`. Bump header to `1.1.0`. |
| `internal/server/server_lifecycle.go` | **Modify.** In `startCacheWarmers` (`:718`), after the `series-warmer` block (ends `:774`), add the `metadata-results-warmer` block exactly as spec D1 shows — `s.bgWG.Add` / `defer s.bgWG.Done` / `defer warmerRecover("metadata-results")` / `if s.bgCtx.Err() != nil { return }`. Bump header. |
| `internal/server/metadata_batch_candidates.go` | **Modify.** `handleListMetadataResults` (`:844`) takes the new `age` return at `:857` and adds `"cache_age_seconds": int(age.Seconds())` to the `gin.H` at `:932-938`. Bump header. |
| `web/src/services/api.ts` | **Modify.** Add `cache_age_seconds?: number;` to `MetadataResultsResponse` (`:3580-3586`). Optional field — no caller changes. Bump header. |

### Signature change to watch

`latestMetadataResultsByBookCached` currently returns
`(map[string]database.OperationResult, map[string]int, error)`. Adding `age` changes
every call site. Verified call sites: `internal/server/metadata_batch_candidates.go:857`
(the only non-test one) and `internal/server/metadata_results_cache_test.go:41`. Confirm
with:

```bash
cd "$WORKTREE"   # e.g. <repo-root>/.worktrees/metadata-results-cold-start
grep -rn "latestMetadataResultsByBookCached" --include="*.go" .
```

If that returns more than those two, update them too before compiling.

### Do NOT

- Do **not** gate the warmer on `WaitForWarmup` / `memReadyChecker`. The build reads
  only `operation:` / `op_result:` keys; there is no memdb path (spec D1).
- Do **not** change `metadataResultsCacheTTL`.
  `TestMetadataResultsCacheTTL_IsShortEnoughToFeelLive`
  (`internal/server/metadata_results_cache_test.go:94`) enforces `5s ≤ TTL ≤ 5m`.
- Do **not** touch `invalidateMetadataResultsCache` (`:86`) in this step — two tests
  depend on its nil-everything semantics.

---

## Step 2 — Stale, not nil, on the three write paths (D3)

One commit. Independent of step 1's shipping decision only in the sense that it can be
reverted alone; it *requires* step 1's stale branch to exist.

### Files

| File | Intent |
|---|---|
| `internal/server/metadata_results_cache.go` | **Modify.** Add `markMetadataResultsCacheStale()`: under `mu`, if `latest != nil`, set `at = time.Now().Add(-metadataResultsCacheTTL - time.Second)`. Leave `invalidateMetadataResultsCache` untouched. Bump header to `1.2.0`. |
| `internal/server/metadata_batch_candidates.go` | **Modify.** Change the three `defer invalidateMetadataResultsCache()` lines — `:478` (`handleBatchApplyCandidates`, declared `:475`), `:583`, `:647` — to `defer markMetadataResultsCacheStale()`. Update each accompanying comment (the one at `:476-477` explains why invalidation exists) to say the list is now served stale-but-labelled for one refresh cycle instead of blocking. Bump header. |

### Why this is not a lie

`CreateOperationResult` (`internal/database/pebble_store_operations.go:469-477`) keys on
`op_result:<OperationID>:<BookID>`, and all three handlers write back with the same
`OperationID` and `BookID` they read (`:540`, `:622`, `:686`) — an in-place row update.
The stale set differs from truth by exactly the statuses the user just changed, for
exactly one cycle, and `cache_age_seconds` from step 1 makes that visible.

---

## Step 3 — ABS contributor warm: enrol + SWR (D4)

One commit. Fully independent of steps 1-2 — different package, different cache.

### Files

| File | Intent |
|---|---|
| `internal/server/wire_abs_routes.go` | **Modify.** Wrap the bare `go func() { ... }()` at `:266-271` in the standard warmer shape: `s.bgWG.Add("abs-contributors-warmer")`, `defer s.bgWG.Done("abs-contributors-warmer")`, `defer warmerRecover("abs-contributors")`, and a `s.bgCtx.Err()` check **both before and after** `ps.WaitForWarmup()` — the exact body is in spec D4 item 1. **Keep** the `ps.WaitForWarmup()` call at `:268`, the comment at `:262-265` that justifies it, and `context.Background()` at `:270`. Bump header. |
| `internal/server/handlers/abs/browse.go` | **Modify.** Add `absContributorsStaleMax` — **proposed** `30 * time.Minute`, a judgment call, not a derived number: long enough that nobody is served a stale contributor list across a working session, short enough that a rebuild wedged for half an hour surfaces as slow rather than silently ancient. See plan open item below. Add an `inflight` guard field. Rewrite `contributorsCached` (`:525`) into fresh / stale / cold, using `h.now()` (field at `internal/server/handlers/abs/handler.go:273`) for all time reads. `WarmContributors` (`:1009`) is unchanged — it just calls `contributorsCached`. Bump header. |
| `internal/server/handlers/abs/handler.go` | **Modify.** Add the `inflight bool` field beside `authorsCacheAt` (`:270`), under the existing `authorsCacheMu` (`:267`). Bump header. |

### Do NOT

- Do **not** remove `ps.WaitForWarmup()`. Unlike the metadata-results build, this one
  is derived from *visible* books (`contributorDTOs` → `h.visibleBookSummaries`,
  `internal/server/handlers/abs/browse.go:613-615`); building pre-memdb caches a library
  that does not exist yet and then serves it for the whole TTL.
- Do **not** thread `s.bgCtx` into `handler.WarmContributors`. `WaitForWarmup`
  (`internal/database/pebble_store.go:153`) is a bare `<-p.warmupDone` with no context,
  so it cannot observe cancellation; `Shutdown` cancels `bgCtx` then calls
  `s.bgWG.Wait()` (`internal/server/server_lifecycle.go:576`), and a cancellable context
  inside the build buys nothing while changing its semantics mid-run. The
  **second `bgCtx` check, placed after `WaitForWarmup` returns**, is what actually
  prevents a 6-second scan against a closing store. Full rationale: spec D4 item 1.
- Do **not** change `absAuthorsCacheTTL` (`:504`).

### Open item for this step

`absContributorsStaleMax = 30 * time.Minute` is a proposed value, not a measured one.
If a reviewer prefers a different bound, change it here — nothing else in the design
depends on the number, only on there *being* a finite bound (spec D2, F2).

---

## Step 4 — Tests

One commit, alongside or immediately after the code. New tests go in existing files
where they exist.

### Files

| File | Intent |
|---|---|
| `internal/server/metadata_results_cache_test.go` | **Modify.** Add: (a) a stale entry is served **without blocking** — prime `at` at `TTL + 5s` ago with a store that would take measurable time, assert the call returns in well under a second and returns the primed map; (b) an entry older than `metadataResultsStaleMax` takes the **cold** branch; (c) `markMetadataResultsCacheStale` leaves `latest` non-nil while making `time.Since(at) >= metadataResultsCacheTTL`; (d) `invalidateMetadataResultsCache` still nils both maps (existing test at `:75` must stay green); (e) concurrent stale reads spawn **one** refresh — a counting fake store plus `-race`. Bump header. |
| `internal/server/cache_warmers_bgwg_test.go` | **Modify.** Extend the existing assertions (`:54`, `:86`) so `metadata-results-warmer` and `abs-contributors-warmer` are both named and both drained by `Shutdown`. Bump header. |
| `internal/server/handlers/abs/browse_contributors_swr_test.go` | **Create.** ABS-side twin: drive `h.now` forward past `absAuthorsCacheTTL`, assert the stale list is returned immediately and a refresh is kicked; drive past `absContributorsStaleMax`, assert the cold branch blocks. Full header (path / version 1.0.0 / fresh guid / last-edited). |

### Commands, and what green means

```bash
cd "$WORKTREE"   # e.g. <repo-root>/.worktrees/metadata-results-cold-start

# 1. The cache itself, with the race detector. THIS IS THE LOAD-BEARING RUN —
#    SWR introduces a detached goroutine writing shared state.
go test ./internal/server/ -run 'MetadataResults' -race -count=1 -v

# 2. The warmer-enrolment contract. Matches the two existing tests
#    TestStartCacheWarmers_SkipOnCanceledCtx and _EnrolledInBgWG.
go test ./internal/server/ -run 'TestStartCacheWarmers' -race -count=1 -v

# 3. The ABS contributor cache.
go test ./internal/server/handlers/abs/... -race -count=1

# 4. Full backend, short — a getter signature change can vacuously pass a
#    subset while breaking a mock elsewhere.
go test ./... -short

# 5. Frontend types.
cd web && npm run build

# 6. Repo CI gate.
cd "$WORKTREE" && make ci
```

**Green means:** (1)-(3) pass with **zero** `DATA RACE` reports and zero
`WARNING: DATA RACE` lines; (4) reports `ok` or `no test files` for every package with
no `FAIL`; (5) exits 0 with no TS errors; (6) `make ci` passes its 30% coverage gate.
Anything less is not green — do not proceed to the acceptance gate on a partial run.

---

## Acceptance gate — the numbers that must be observed

There is no apply in this workstream, so the gate is observational rather than
dry-run/apply. **All six observations below must hold on a real restart before this is
called working.** `<server>` stands in for the deployment host; use
`https://<server>:8484`.

1. **Boot warm fires and completes before any user request.** In the logs after a
   restart, `metadata-results cache rebuilt` appears with `trigger=boot`, and its
   timestamp precedes the first `GET /api/v1/library/metadata-results` access-log line.
   **Record** its `duration_ms` for the record — the spec's §1.1 observations put the
   build at 21,000–34,000 ms, but a faster host legitimately landing well under that is
   not a failure, so this is not a pass/fail threshold. The correctness check is item 2,
   which is. Only one combination warrants a look before accepting: a sub-second
   `duration_ms` **together with** a `books=` count near zero, which means the build
   found nothing and the warm is proving nothing.
2. **No rows lost.** The `books=` count on the `trigger=boot` line must equal the
   `books=` count on the next `trigger=demand` or `trigger=refresh` line taken from the
   same store with no fetch operation in between. Reference from the code comment:
   `36,805` results folded to the latest-per-book set. A **drop is a hard stop** — do
   not ship.
3. **First request after restart is fast.** Time the first call:
   ```bash
   curl -s -o /dev/null -w '%{time_total}\n' \
     -H "Authorization: Bearer $ABK_TOKEN" \
     'https://<server>:8484/api/v1/library/metadata-results?limit=3'
   ```
   Must be **under 0.5 s**, against the 34 s baseline. Its `cache_age_seconds` must be
   greater than 0 (proving it was served from the boot warm, not built on demand).
4. **A minute-5 arrival is also fast.** Repeat the same curl at T+5 min (well past the
   60 s TTL). Must still be **under 0.5 s**, and the logs must show a
   `trigger=refresh` rebuild starting at that moment. This is the observation that
   distinguishes SWR from a bare boot warm — a bare warmer fails here at ~34 s.
5. **Post-apply is fast.** Apply a small batch through the match UI, then immediately
   reload the list. Must be **under 0.5 s** (today: a full cold build). The applied
   books show their pre-apply status for at most one refresh cycle, and
   `cache_age_seconds` on that response is non-zero.
6. **ABS side.** `abs: contributor cache warmed` appears in the boot logs with a
   `duration_ms` near the recorded **6,104 ms** cold figure. A contributor page request
   at T+6 min (past the 5-minute TTL) returns near the **105 ms** warm figure rather
   than ~6 s.

Additionally, `journalctl`/service logs must show **no** `pebble: closed` panic and no
`cache warmer panicked` line for `metadata-results`, `metadata-results-refresh` or
`abs-contributors` across a full start → serve → `Shutdown` cycle.

---

## Rollback

Every step is a separate commit with a self-contained revert.

| Step | Revert effect |
|---|---|
| 4 (tests) | No production behaviour change. |
| 3 (ABS) | Returns to today's bare `go func()` warm + hard 5-minute TTL. No data implication. |
| 2 (stale-not-nil) | Returns to `invalidateMetadataResultsCache` on the three write paths — the 34 s post-apply block returns, nothing is corrupted. |
| 1 (SWR + warmer) | Returns to the plain 60 s memo with no boot warm. `cache_age_seconds` disappears from the response; the TS field is optional so the frontend keeps compiling. |

**No data migration, no backfill, nothing persisted.** The entire change is in-process
cache state that is rebuilt from `operation:` / `op_result:` on the next read. A
rollback at any point is a binary swap plus a restart — there is no state to unwind.

---

## Post-task hygiene (per `CLAUDE.md`)

- Add a fragment under `changelog.d/` (e.g. `20260805_223000_metadata_results_swr.md`).
  **Do not hand-edit `CHANGELOG.md`.**
- Tick the item in `TODO.md` (checking off is a normal direct edit; only *new* tasks go
  through `todo.d/`).
- **Executive summary: not required.** Against
  `docs/process/executive-summaries.md`'s criteria this is a single-PR performance fix
  with no data-loss or corruption exposure and no multi-PR tracked set. `CHANGELOG` +
  `TODO` are sufficient.
