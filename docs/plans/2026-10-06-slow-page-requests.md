<!-- file: docs/plans/2026-10-06-slow-page-requests.md -->
<!-- version: 1.1.0 -->
<!-- guid: 5b7e2c1a-9d43-4f6e-8a21-3c0f7d9e4b85 -->
<!-- last-edited: 2026-10-06 -->

# Plan — every page request under 3 s on a warmed prod server

Branch `perf/slow-page-requests`, worktree `aorg-slow-requests`, based on
origin/main `1bb336b75`. Approved by the owner before writing; this file
records the per-endpoint diagnosis and the fixes.

## Evidence first: restarts versus persistent cost

`process_start_time_seconds{job="audiobook-organizer"}` shows **7 prod restarts
in the last 7 hours** (1791316662, 1791323287, 1791324015, 1791332667,
1791335404, 1791338183, 1791341080). Lining Tempo's slow traces up against them
splits the complaints into two kinds:

| Endpoint | Slow traces | Kind |
|---|---|---|
| `GET /system/status` | 6–24 s continuously, including 1,900 s after a restart | **persistent** |
| `GET /audiobooks/soft-deleted` | 31–58 s at restart+1,200…2,100 s; 733 s at restart+28 s | **persistent** (worse cold) |
| `GET /audiobooks/metadata/cached` | 10–42 s, 1,600 s after a restart | **persistent** |
| `GET /audiobooks/facets` | one 29 s call at restart+28 s | cold start only |
| `GET /metadata/cache/review` | 64–125 s, each within ~90 s of a restart; warm calls 2.6–4.2 s | cold start + ~1 s over budget warm |
| `GET /audiobooks` | 14 identical requests in one second, 44–160 s each, all within 5 min of a restart | cold start herd |

No spans below the otelgin root exist (Tempo has only the HTTP span), so the
hot paths below come from reading the code, confirmed with local benchmarks on
synthetic fixtures.

## 1. `GET /api/v1/system/status` (polled; 32 of 75 calls > 10 s)

**Today.** `system.Handler.GetSystemStatus`
(internal/server/handlers/system/handler.go:188) →
`sysinfo.SystemService.CollectSystemStatus` (internal/sysinfo/service.go:130).
The counts come from `GetDashboardStats`, which is stale-while-revalidate and
cheap. The Dashboard's "Recent Operations" panel calls
`ListOperationsV2Since(time.Time{}, 5)` (service.go:157).

**Why it's slow.** With a zero `since`, `opv2ULIDSeekBound` returns the bare
`opv2:op:` prefix. Leg 3 of `listOperationsV2SinceIndexed`
(pebble_store_ops_v2_timeline.go:459) therefore JSON-decodes **every operation
row ever written**, sorts them all and keeps 5. The timeline file's own header
measured this at 5.46–5.95 s on prod, and the table grows by about 1,780 rows a
day. Library.tsx calls status on every filter change and the Dashboard polls
it, so the calls pile up on each other (about 10 in the same second at
1791340091, each 20 s).

**Fix.** Add `database.ListRecentOperationsV2(lister, limit, now)`. It asks for a
window `since = now − W` starting at W = 1 h and doubling. The answer is exact
once the window holds at least `limit` rows with `StartedAt ≥ since`:

- A row outside the window has `CompletedAt < since` (and `QueuedAt < since`).
- So its `StartedAt` is either nil (sorted last) or before `since`.
- Either way it sorts below every in-window row that started inside the window.

If doubling reaches 1 year without enough rows, it falls back to the zero
window, which gives today's answer. While the timeline index is untrusted (from
boot until reconcile), every windowed call is a full scan anyway. In that state
it makes one zero-window call instead of doubling.

**Expected.** About 75 rows decoded per poll instead of every row ever written:
milliseconds.

**Risk.** Low. An equivalence test compares it against the full answer on
randomized fixtures. The fixtures include open ops started long ago, ops
queued but never started, and ops that completed earlier than they were queued
(clock skew).

**No singleflight here, deliberately.** The handler writes
`status.PluginHealth` into the returned struct, so sharing one result between
callers would be a data race. After the fix each call is cheap enough not to
need it.

## 2. `GET /api/v1/audiobooks/soft-deleted` (mean 41 s → 138 s in the last 3 h)

**Today.** `ListSoftDeletedAudiobooks` (handlers/audiobooks/handler.go:806)
does three things:

- `GetSoftDeletedBooks(limit=1)`. The Library header asks for `limit=1` and
  reads only `total`.
- `CountSoftDeletedBooks`, implemented in internal/audiobooks/service_single.go:376.
- `TrashProgress` on the page.

**Why it's slow (root cause).**

- `database.AsSoftDeletedCountStore` (soft_deleted_count.go:32) is a **bare
  type assertion**. Every other `As*Store` helper uses `AsCapability`.
- The audiobook service is built with `indexedStore`
  (`Override("store", resolvedStore)`, server.go:648). `indexedStore` embeds
  `database.Store`, and `CountSoftDeletedBooks` is not on that interface.
- So the assertion **always misses in prod**, and the count takes the "mocks
  only" fallback: it pages `ListSoftDeletedBooks(1000, offset)` until the trash
  is exhausted. That is 49 calls.
- Each of those 49 calls copies all 48,012 `Book` structs out of memdb and
  stable-sorts them by value.
- If memdb reports incomplete, each call is instead a full Pebble scan, which
  matches the 733 s measured at restart.

**Fix.**

- (a) `AsSoftDeletedCountStore` → `AsCapability`. This is a read, so going past
  the reindexing decorator is safe.
- (b) In memdb `ListSoftDeletedBooks`, sort `*Book` pointers and copy only the
  requested page. Today it copies 48k structs to return 1.
- (c) memdb `CountSoftDeletedBooks` gets the same `requireTablesComplete` guard
  and Pebble fall-through that List has, so the count and the list it paginates
  cannot disagree.
- (d) A regression test that goes **through `indexedStore`**. A raw PebbleStore
  would pass while prod stays broken.

**Expected.** Count is one memdb index walk (about 48k pointer reads, ms) and
the page copy is a single Book: well under 100 ms.

**Risk.** Low. Same answers, fewer copies.

## 3. `GET /api/v1/audiobooks/metadata/cached` (mean 29 s)

**Today.** `ListCachedCandidates` (handlers/metadata_cache.go:301). The only
caller (the Library chip) asks `limit=1` and reads `total`. The handler:

- calls `ListCachedSummaries` → `PebbleStore.ListMetadataCacheKeys`
  (pebble_store_metadata_cache.go:92);
- calls `GetBooksByIDs` over every summary;
- builds a `gin.H` per row.

**Why it's slow.**

- `ListMetadataCacheKeys` fully decodes every `metadata_cache:` entry,
  including every candidate's description and the rest, only to take
  `len(Candidates)`. That is about 40–56k large JSON documents.
- `GetBooksByIDs` is two Pebble point reads plus a JSON decode per book (with
  signature hydration) for about 40k books.
- Both costs are per request.

**Fix.**

- (a) **In-process summary index on PebbleStore**, kept current through the
  existing metadata-cache change log:
  - Build it once with a full scan, reading `MetadataCacheGeneration` before the
    scan.
  - After that, each call asks `MetadataCacheChangedSince(gen)` and point-reads
    only the named rows.
  - If the log can't vouch for the changes (Reset or overrun), it rebuilds.
  - The sorted slice is cached until a change lands.
  - Every caller of `ListMetadataCacheKeys` benefits: this endpoint, the review
    snapshot's full build, the stale filter, bulk-apply preview, batch claims,
    the reap op and unfetched-candidates.
- (b) The full scan decodes into a light struct (`Candidates []json.RawMessage`)
  instead of the full candidate list.
- (c) The handler reads only Title + review status through a new memdb-backed
  capability, `database.BookListingFieldsReader`. It falls back to Pebble when
  memdb is unavailable or incomplete, and also when the store lacks the
  capability.
- (d) The handler builds `gin.H` only for the requested page.

**Expected.** Warm: one memdb pointer read per row plus sorting of cached
summaries, in the low hundreds of ms for 56k rows. The first call after boot
pays one scan, which is cheaper than today because of the light decode.

**Risk.** Medium. It is a new cache, and its correctness depends on the change
log covering every writer. That is already relied on by the review snapshot.
Tests cover:

- Put, Delete and the UpdateBook identity delete reflected on the next call;
- Reset → rebuild;
- a write during the build;
- equivalence with the scan.

## 4. `GET /api/v1/audiobooks/facets`: out of scope, handed off

The coordinator moved this endpoint to branch `feat/scoped-tag-facets`, which
is rewriting facets to be scoped to the current search, and that branch owns
its performance. **No facets change is in this branch.** Findings handed over:

- The one slow call took 29 s, 28 s after a restart, on a cold cache.
- `GetDistinctGenres`/`GetGenreCounts` and `GetDistinctLanguages`
  (pebble_store.go ~4364–4420) are unconditional Pebble full scans. They have
  no memdb dispatch, unlike `GetDistinctPublishedYears`.
- The boot warmer does not wait for memdb.
- The miss path has no singleflight.
- The Pebble versions count soft-deleted books too, so genres that exist only
  on trashed books are listed (see D3).

## 5. `GET /metadata/cache/review` (warm 2.6–4.2 s; cold 64–125 s after restarts)

The memory note
(`project_review_page_incremental_snapshot_design.md`) says the incremental
snapshot is "NOT built". **That is stale.** `buildIncremental`,
`overlayLiveBooks` with `BooksChangedSince`, and `MetadataCacheChangedSince`
all exist (metadata_cache_snapshot.go v2.1.0). What is left:

- **Cold build after restart.** Fix 3(a)/(b) removes one of the two
  whole-cache decodes in the full build. The chunk loop still decodes every
  entry, because it needs the candidates.
- **Warm calls at about 1 s over budget.** No phase breakdown exists for the
  prod requests, and the slow-listing log only fires at ≥ 5 s.

**Not changed in this branch.** I filed a scoped TODO fragment instead:

- take the phase timings (snapshot / overlay / prepare / encode) from the
  ≥ 5 s log on prod after this branch deploys;
- then target the largest phase.

I'm not guessing at it without measurements.

## 6. `GET /api/v1/audiobooks` outliers (5 of 351 > 10 s)

Every one over 10 s is a cold-start herd: 14 identical requests in the same
second, each 44–160 s, while memdb warms and the list cache is cold. The
frontend's retry loop re-issues the same query.

**Fix.** A singleflight on the list-cache key, so identical cache misses share
one build. It applies only when the response is cacheable, and never with
`Prefer: respond-async` or `allow-stale`, since those change the response
shape.

- The builder runs under `context.WithoutCancel`, so one client leaving does
  not cancel the others.
- `applied_filters` is set inside the shared closure, so no caller mutates a
  shared map.

This does not make a cold build fast. It stops 14 copies of it from competing
for the same CPU.

## Ordered steps (one commit each)

1. PLAN.md (this file).
2. status: `ListRecentOperationsV2` + sysinfo switch + equivalence test + benchmark.
3. soft-deleted: AsCapability, pointer-sort page copy, count guard, test via indexedStore.
4. metadata/cached: summary index + light decode + listing-fields capability + handler page-only build + tests.
5. ~~facets~~: handed off to feat/scoped-tag-facets; not in this branch.
6. /audiobooks: list-cache singleflight + test.
7. changelog.d fragment + todo.d fragment (review phase measurement).

## Test strategy

- `go build ./...` and `go vet` on every touched package.
- Targeted `go test -race -run` on the touched packages, with exact counts
  reported.
- Each fast path has a test that fails on today's code:
  - status: equivalence and "decodes far fewer rows than exist" (benchmark with
    a 50k-row synthetic opv2 table);
  - soft-deleted: count resolves through `indexedStore`;
  - cached: index stays exact across writers;
  - list: identical concurrent misses run one build.

## Rollback

Every change is behind existing interfaces, with no persisted format change and
no new keys. Rollback is `git revert` of the individual commit. The summary
index is in-process only; a restart rebuilds it.

## Decisions for owner to validate

- **D1 — Why is prod restarting about hourly?**
  - What happened: 7 restarts in 7 hours.
  - Why it matters: every restart throws away memdb (about 130 s warm-up), the
    review snapshot, the list cache and the summary index. Most of the
    60–160 s requests in your traces are the first minutes after a deploy.
  - Recommendation: if these are deploys, batch them. If any are crashes,
    that's a separate incident.
- **D2 — Recent Operations exactness.**
  - What changes: the windowed read is provably identical to today's
    "newest 5 by started_at".
  - The assumption it rests on: `StartedAt ≤ CompletedAt` on a single clock.
    A row that breaks it could be missed until the window widens.
  - Why it's acceptable: I think so (single host, one clock), but it is an
    assumption.
- **D3 — Genre/language facets include soft-deleted books.**
  - What's wrong today: the Pebble versions count every book row, so genres
    that only appear on the 48k trashed books show up in the Library facets.
    The interface doc says "primary books".
  - Who decides: whoever owns feat/scoped-tag-facets. This branch does not
    touch facets.
- **D4 — `metadata/cached` default limit.**
  - Today: it still defaults to "all rows" when `limit` is absent. That
    contract was kept on purpose.
  - What this branch does: the fix makes it cheap regardless, so it stays
    unchanged.
- **D5 — Review page warm latency.**
  - What's deferred: the next step needs a prod phase measurement first (TODO
    fragment).
  - Why: guessing would risk optimizing the wrong phase.
