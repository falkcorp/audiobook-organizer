<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-214-cap-get-api-v1-audiobooks-metadata-cache-review-.md -->
<!-- version: 1.1.0 -->
<!-- guid: 9d1903e4-8b0b-44e5-939b-d5390332acf0 -->
<!-- last-edited: 2026-09-02 -->

# TASK-214 — Cap GET /api/v1/audiobooks/metadata/cache/review to a default page size, add all=true escape hatch, and log when it exceeds 5s (REV-EMPTY-2)

> **Status 2026-09-02:** 🟡 OPEN — still worth doing — metadata_cache.go:168 limit := ParseQueryInt(c,'limit',0) unchanged; 0 hits for an 'all' query param; useMetadataLane.ts:537 still calls getCachedReviewResults(0, 0); no 5s slow-log. Recommendation: keep.

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Sonnet-class · server-handlers subagent · **Why:** Requires widening a shared narrow interface plus regenerating (or hand-mirroring) a mockery-managed mock file correctly, adding a testable pure helper for the scan-active check, and building a >200-row Go test fixture -- more surface and more ways to get the mock/interface sync wrong than a Haiku pass should be trusted with. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 90021 as of commit 46628240 (later edits shift lines) — re-find it with `sed -n '90021p' TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-20.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-214-cap-get-api-v1-audiobooks-metadata-cache-review-" -b agent/server-handlers-214-cap-get-api-v1-audiobooks-metadata-cache-review- origin/main
cd "$REPO/.worktrees/server-handlers-214-cap-get-api-v1-audiobooks-metadata-cache-review-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

GetCacheReviewResults (internal/server/handlers/metadata_cache.go) must not hand back the entire cache to a caller that does not explicitly ask for it. When `limit` is 0 or absent AND the new `all` query param is not `true`, treat the request as if `limit=200` had been sent (existing total_count/matched/etc. reporting is unaffected, since those are already computed over the full `reviewable` set independent of the page slice). When `all=true` is sent, preserve today's literal-everything behaviour exactly, because the one existing caller (useMetadataLane.ts, client-side filter/group/stale derivation spanning the whole library) depends on it and must keep working unchanged -- update that caller to pass `all=true` explicitly. Separately, add a WARN log line whenever the handler's own processing exceeds 5 seconds, including the measured duration and whether a `library.scan` op is currently queued or running, so a future slow request can be diagnosed instead of only reported.

## Background (verify before editing)

- internal/server/handlers/metadata_cache.go's GetCacheReviewResults computes `reviewable` (every cache row this endpoint COULD return) up front regardless of limit/offset -- pagination only slices `reviewable[start:end]` afterwards (L343-354) -- and `total_count`/`matched`/`no_match`/`stale`/`unreviewable*` are all derived from the full `reviewable`/`prepared` sets, never from the page. So capping the default page size changes only how many `results` rows come back in one response; it does not change any of the summary counts the rail displays.
- The expensive part of this handler that the file's own comments already call out (L189-195, L256-270) is NOT the per-page BuildCandidateBookInfo work (that already only runs over `page`, L356-380) -- it is the concurrent GetCachedCandidates read over EVERY prepared row (L271-284, bounded to reviewListConcurrency=8), which runs unconditionally regardless of limit. Capping the page size therefore reduces response PAYLOAD size and BuildCandidateBookInfo cost, but will NOT by itself eliminate the 34.8s/18.4s prod timings if those were dominated by the cache-read fan-out contending with a concurrent library.scan -- which is exactly why the WARN timing log is the other half of this task: it turns 'sometimes slow' into a measurable, correlatable signal instead of a one-off prod log line.
- The sole web caller, useMetadataLane.ts:410 (`api.getCachedReviewResults(0, 0)`), is deliberate: its own JSDoc (L318-321) explains the lane fetches ALL rows once and paginates/filters/groups/derives staleIds client-side across the WHOLE library, because per-page derivation would miss cross-page duplicates and could not report 'X stale of Y total' accurately. This is the caller the scope brief's conditional ('keep limit=0 only behind an explicit all=true if something depends on it') is asking about, and the grep above proves something does depend on it.
- MetadataCacheBookStore (the narrow interface this handler is built against) does not currently expose any operations-listing method. The concrete value always passed in (`s.storeForWiring()`, a `database.Store`) already implements `ListActiveOperationsV2() ([]OperationV2Row, error)` (internal/database/iface_ops_v2.go:140) as part of the wider `database.OpsV2Store` interface it embeds -- so this task widens `MetadataCacheBookStore` by exactly that one method rather than injecting a new dependency or reaching for the wide `OpsV2Store`.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "limit := httputil.ParseQueryInt" internal/server/handlers/metadata_cache.go   # 1 hit, L168: `limit := httputil.ParseQueryInt(c, "limit", 0)` — the handler parses limit/offset with ParseQueryInt, default 0, and never caps a 0/absent limit
  grep -n "if limit > 0 {" internal/server/handlers/metadata_cache.go   # 1 hit, ~L348, inside the pagination block right before `page := reviewable[start:end]` — the page slice only truncates when limit > 0, so limit=0 (or absent) returns every reviewable row today
  grep -n '"total_count": len(reviewable)' internal/server/handlers/metadata_cache.go   # 1 hit, ~L384, in the final httputil.RespondWithOK gin.H{...} — total_count is computed from len(reviewable) independent of the page slice, so capping the page will not change total_count/matched/no_match/stale/etc reporting
  grep -rn "getCachedReviewResults(" web/src --include=*.ts --include=*.tsx   # 2 hits: web/src/services/api.ts:3734 (the function definition) and web/src/components/review/lanes/useMetadataLane.ts:410 (the sole call site, `.getCachedReviewResults(0, 0)`) — the ONLY web caller of this endpoint sends limit=0, offset=0
  grep -n "the lane fetches with limit=0" web/src/components/review/lanes/useMetadataLane.ts   # 1 hit at L319 — comment spans two lines; the client depends on limit=0 returning every row — that caller's own comment explains WHY it needs every row in one call: client-side filtering/grouping/staleIds span the whole library, not one page
  grep -n "func ParseQueryBool" internal/httputil/parse.go   # 1 hit, L44: `func ParseQueryBool(c *gin.Context, key string, defaultValue bool) bool` — httputil already has a bool query-param parser to reuse for all=true, so no new parsing helper is needed
  grep -n "NewMetadataCacheHandler(s.storeForWiring()" internal/server/wire_handlers.go   # 1 hit, L66 — the concrete store already implements ListActiveOperationsV2 (used to detect an active library.scan), and it is already wired into this handler's constructor via storeForWiring(), so only the narrow interface needs widening -- no new dependency to inject
  grep -n "ListActiveOperationsV2 returns ops with status 'queued' or 'running'" internal/database/iface_ops_v2.go   # 1 hit, ~L139, the doc comment directly above the interface method at L140 — ListActiveOperationsV2 returns only queued/running rows already, so no extra status filter is needed when checking for an active scan
  grep -n 'ID:.*"library.scan"' internal/server/library_core_ops.go   # 1 hit, ~L68 — library.scan's v2 OperationDef ID is exactly the literal to match against DefID
  grep -n "MetadataCacheBookStore:" .mockery.yaml   # 1 hit, ~L213 — MetadataCacheBookStore is registered in .mockery.yaml so `make mocks` regenerates its mock after the interface is widened
  grep -n 'reviewCtx("limit=0&offset=0")' internal/server/handlers/metadata_cache_test.go   # 3 hits across TestGetCacheReviewResults_CountsOnlyReviewableRows, _UnreviewableSplitByCause, _FlagsStaleRows — an existing Go test already exercises this handler with limit=0 and a small (2-4 row) fixture, so it will keep passing unchanged once limit=0 defaults to a 200-row page (2-4 < 200)
  ```

### Reuse — don't invent

- Use `httputil.ParseQueryBool(c, key, defaultValue)` in `internal/httputil/parse.go` (verify: `grep -n "func ParseQueryBool" internal/httputil/parse.go`) — do NOT write a parallel helper.
- Use `reviewCtx(query string) test helper (build a GET gin.Context for this endpoint)` in `internal/server/handlers/metadata_cache_test.go` (verify: `grep -n "func reviewCtx" internal/server/handlers/metadata_cache_test.go`) — do NOT write a parallel helper.
- Use `the metadata_batch_candidates.go pattern of checking recent/active ops by def/type before acting -- same idea, different store method (ListActiveOperationsV2 vs GetRecentOperations) because this handler only has the narrow MetadataCacheBookStore, not the full Ops() store` in `internal/server/metadata_batch_candidates.go` (verify: `grep -n "activeOps, _ := store.GetRecentOperations(50)" internal/server/metadata_batch_candidates.go`) — do NOT write a parallel helper.
- Use `MockStore_ListActiveOperationsV2 -- the mockery-generated template for a no-arg two-return-value method, to copy the shape from if `make mocks` cannot be run in the environment` in `internal/database/mocks/mock_store.go` (verify: `grep -n "func (_mock \*MockStore) ListActiveOperationsV2" internal/database/mocks/mock_store.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/server/handlers/metadata_cache.go, add a package-level const near `reviewListConcurrency` (L37): `// defaultReviewPageSize bounds a review-listing request that does not explicitly\n// ask for everything. limit=0/absent used to mean "return every row", measured\n// at 34.8s and 18.4s in production while a library.scan ran concurrently; a\n// caller that forgets to page (or a stray curl) must not pay for the whole cache.\nconst defaultReviewPageSize = 200`.
2. In GetCacheReviewResults, right after the existing `limit`/`offset` parsing and clamping (L168-175), add: `all := httputil.ParseQueryBool(c, "all", false)` then `if limit == 0 && !all { limit = defaultReviewPageSize }`. Leave everything else in the function (the `reviewable` build-up, the summary counts, the `start`/`end`/`page` slicing at L343-354) untouched -- it already respects a positive `limit` correctly.
3. Widen the `MetadataCacheBookStore` interface (L39-48) by adding one method: `// ListActiveOperationsV2 backs the diagnostic "is a library.scan running" check\n// logged when GetCacheReviewResults exceeds 5s. The concrete store passed in\n// (database.Store, via storeForWiring()) already implements this.\nListActiveOperationsV2() ([]database.OperationV2Row, error)`.
4. Add a small exported helper function in metadata_cache.go, below the interface: `// LibraryScanActive reports whether a "library.scan" op is currently queued or\n// running. Diagnostic only: a nil store or a lookup error is reported as "not\n// scanning" rather than aborting or erroring the caller.\nfunc LibraryScanActive(store MetadataCacheBookStore) bool {\n\tif store == nil {\n\t\treturn false\n\t}\n\tactive, err := store.ListActiveOperationsV2()\n\tif err != nil {\n\t\treturn false\n\t}\n\tfor _, op := range active {\n\t\tif op.DefID == "library.scan" {\n\t\t\treturn true\n\t\t}\n\t}\n\treturn false\n}`.
5. At the very top of GetCacheReviewResults's body (right after the `h.store == nil || h.svc == nil` guard, before `limit := httputil.ParseQueryInt(...)`), add `start := time.Now()` (the `time` package is already imported in this file).
6. Immediately before the final `httputil.RespondWithOK(c, gin.H{...})` call (L382), add: `if d := time.Since(start); d > 5*time.Second {\n\tslog.Warn("GetCacheReviewResults exceeded 5s",\n\t\t"duration", d.Round(time.Millisecond),\n\t\t"total_reviewable", len(reviewable),\n\t\t"returned", len(results),\n\t\t"library_scan_active", LibraryScanActive(h.store),\n\t)\n}` (`slog` is already imported).
7. Run `make mocks` (pinned mockery v3.7.1; install via `bash scripts/setup-mockery.sh` if the binary is missing) to regenerate internal/server/handlers/mocks/mock_metadata_cache_book_store.go with the new `ListActiveOperationsV2` method. If mockery cannot run in this environment, hand-add the method by copying the exact shape of `MockStore_ListActiveOperationsV2` from internal/database/mocks/mock_store.go (lines ~20160-20213: the direct method, the `MockMetadataCacheBookStore_ListActiveOperationsV2_Call` type, the `.EXPECT().ListActiveOperationsV2()` builder, `.Run()`, `.Return()`, `.RunAndReturn()`), renaming `MockStore`/`_e.mock`/etc. to match `MockMetadataCacheBookStore`'s existing naming convention in that file, and changing the return type import qualifier from bare `OperationV2Row` to `database.OperationV2Row` (the mock package already imports `database`).
8. In web/src/services/api.ts, change `getCachedReviewResults`'s signature (L3734) to accept a third parameter: `export async function getCachedReviewResults(limit: number, offset: number, all = false): Promise<{...}>` (keep the existing return type unchanged), and change the URL build (L3769-3771) from the template literal to: `const params = new URLSearchParams({ limit: String(limit), offset: String(offset) }); if (all) params.set('all', 'true'); const response = await apiFetch(\`${API_BASE}/audiobooks/metadata/cache/review?${params}\`);`.
9. In web/src/components/review/lanes/useMetadataLane.ts, change the sole call site (L410) from `.getCachedReviewResults(0, 0)` to `.getCachedReviewResults(0, 0, true)`, preserving today's 'fetch everything, paginate client-side' behaviour exactly.
10. Bump version headers: metadata_cache.go (currently 1.7.1 -> 1.7.2), metadata_cache_test.go (2.1.0 -> 2.2.0), api.ts (2.69.0 -> 2.70.0), useMetadataLane.ts (1.4.0 -> 1.4.1); all with today's date. Do NOT hand-edit the mock file's header -- mockery does not manage one, leave it as `// Code generated by mockery; DO NOT EDIT.`
11. In internal/server/handlers/metadata_cache_test.go, add `"fmt"` to the import block (needed for the new fixture's id generation).
12. Add TestGetCacheReviewResults_DefaultsToPageSizeWhenLimitAbsent: build a 205-entry fixture (loop `i := 0; i < 205; i++`, ids like `fmt.Sprintf("b%03d", i)`, one `metafetch.MetadataCacheSummary{BookID: id}` and one `database.Book{ID: id}` per id), `svc.EXPECT().ListCachedSummaries(mock.Anything).Return(summaries, nil)`, `store.EXPECT().GetBooksByIDs(mock.Anything).Return(books, nil)`, `store.EXPECT().GetBookFiles(mock.Anything).Return(nil, nil).Maybe()`, one shared `withCandidate` fixture and `svc.EXPECT().GetCachedCandidates(mock.Anything).Return(withCandidate, true, nil)` (matches every id). Call `h.GetCacheReviewResults(c)` with `reviewCtx("")` (no query at all -- exercises the 'absent' branch, not just literal limit=0). Assert `len(body.Data.Results) == 200` and `body.Data.TotalCount == 205`.
13. Add TestGetCacheReviewResults_AllTrueReturnsEverything: same 205-entry fixture (extract the fixture-building into a small local helper or duplicate it -- either is fine), call with `reviewCtx("all=true")`, assert `len(body.Data.Results) == 205` (the full set, proving `all=true` still bypasses the new default).
14. Add TestLibraryScanActive_DetectsRunningScan and TestLibraryScanActive_FalseWhenNoneActive: construct a `handlersmocks.NewMockMetadataCacheBookStore(t)`, `.EXPECT().ListActiveOperationsV2().Return([]database.OperationV2Row{{DefID: "library.scan", Status: "running"}}, nil)` for the true case (assert `handlers.LibraryScanActive(store)` is true) and `.Return(nil, nil)` for the false case (assert false). Also add TestLibraryScanActive_NilStoreIsFalse asserting `handlers.LibraryScanActive(nil)` is false without panicking.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_214.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- limit=0&all=true must return literally everything, unbounded by defaultReviewPageSize -- this is the exact case the sole existing caller relies on; do not let `all` only raise the cap to some larger number.
- A positive explicit `limit` (e.g. `limit=50`) must behave exactly as before, `all` is irrelevant to it -- only the limit==0 branch is touched.
- LibraryScanActive must never error out or panic the response path -- it is diagnostic-only, called only inside the WARN branch, and swallows its own store error into `false`.
- Do not add a status filter inside LibraryScanActive -- ListActiveOperationsV2's own contract (per its doc comment) already restricts to queued/running rows, so re-checking op.Status here would be redundant and could silently diverge from the store's contract if that doc comment is ever wrong without this code failing loudly.

## Tests

- internal/server/handlers/metadata_cache_test.go: TestGetCacheReviewResults_DefaultsToPageSizeWhenLimitAbsent -- 205 reviewable rows, request with no limit/all params, asserts exactly 200 results returned but total_count still reports 205.
- internal/server/handlers/metadata_cache_test.go: TestGetCacheReviewResults_AllTrueReturnsEverything -- same fixture, `all=true`, asserts all 205 rows returned (the anti-over-suppression case: proves the one real caller's behaviour is fully preserved).
- internal/server/handlers/metadata_cache_test.go: TestLibraryScanActive_DetectsRunningScan / _FalseWhenNoneActive / _NilStoreIsFalse -- pins the diagnostic helper in isolation (the 5s-WARN threshold itself is wall-clock-dependent and intentionally not unit-tested; acceptance for that half is a code-level check, see 'acceptance').

Anti-over-suppression test: `TestGetCacheReviewResults_AllTrueReturnsEverything` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/handlers/... ./internal/server/handlers/mocks/... -count=1 && npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go build ./... && go vet ./... && go test ./internal/server/handlers/... -count=1` passes.
- [ ] `git diff --stat internal/server/handlers/mocks/mock_metadata_cache_book_store.go` shows the file changed (new ListActiveOperationsV2 method added) after `make mocks`; `make mocks-check` is clean.
- [ ] `grep -n 'if d := time.Since(start); d > 5\*time.Second' internal/server/handlers/metadata_cache.go` returns 1 hit, confirming the WARN guard landed.
- [ ] `grep -n '.getCachedReviewResults(0, 0, true)' web/src/components/review/lanes/useMetadataLane.ts` returns 1 hit -- the one real caller opted into the full-set behaviour explicitly.
- [ ] `npm --prefix web run lint && npm --prefix web run build --workspace web` (or `make build-api`/`make ci` per the repo's own gate) type-checks the new third `getCachedReviewResults` parameter cleanly.
- [ ] Anti-over-suppression test: `TestGetCacheReviewResults_AllTrueReturnsEverything` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/handlers/... ./internal/server/handlers/mocks/... -count=1 && npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_214.md`.

## Commit message

```
refactor(server-handlers): Cap GET /api/v1/audiobooks/metadata/cache/review to a defaul (REV-EMPTY-2)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Shares web/src/components/review/lanes/useMetadataLane.ts with todo_line 90022 (evidence panel task) -- that task edits the spineCtx useMemo (~L860-871) for an unrelated onRefetch field, this task edits the fetch call site (L410) for the third getCachedReviewResults arg. Different lines, low collision risk, but flag for the coordinator to serialize or merge if both land in the same worktree. Per owner decision #14 in SCOUT-INSTRUCTIONS.md, this is a code-build task (the endpoint itself is always safe to call, read-only); it is not a 'prod_run' item -- no production trigger is needed, it ships as a normal deploy.
