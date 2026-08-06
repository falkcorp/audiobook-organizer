<!-- file: docs/specs/2026-08-05-metadata-results-cold-start-design.md -->
<!-- version: 3.0.0 -->
<!-- guid: 8a3492bd-5c2e-4b8f-93d0-ae9232a3fdda -->
<!-- last-edited: 2026-08-05 -->

> ⚠️ **UNVERIFIED DRAFT — symbols not grep-checked.** Authored by an agent on
> 2026-08-05; the adversarial verification pass did not run (the workflow was
> halted by API rate limiting). Treat every code citation as a claim, not a fact.
> The design reasoning and measured production numbers are sound; the code
> references need checking before execution.


# Design — kill the cold start on the metadata-results build (and its twin, the ABS contributor build)

Owner item 6, 2026-08-05.
Source fragment: [`todo.d/20260805_220400_metadata_results_cold_start.md`](../../todo.d/20260805_220400_metadata_results_cold_start.md).

> **Note on the two context notes named in the workstream brief.**
> `.claude/notes/2026-08-05-first-aid-architecture.md` and
> `.claude/notes/2026-08-05-unlinked-books-investigation.md` are **not present in this
> worktree** — `.claude/notes/` here contains only `2026-07-31-fable-session-log.md` and
> `2026-08-01-sso-continuation-prompt.md`. They were therefore not read while writing
> this revision, and nothing here depends on them. That is defensible for this
> workstream specifically: it has no apply path, writes no files, deletes nothing, and
> never touches book or book_file rows (§5). Any later revision that gains a write path
> must re-read them first.

This is the **smallest** workstream in the 2026-08-05 set and the spec is deliberately
short. It is longer than "add a warmer" for exactly one reason: warming a **60-second**
TTL cache at boot buys 60 seconds of relief and nothing more. Three other defects
turned up during verification; all three are recorded as **known defects (§7)** rather
than folded into the work, so the plan stays at four steps.

---

## 1. Problem

### 1.1 The numbers, and where each one comes from

Every figure below was read out of the tree at the cited location, or comes from the
owner's own fragment.

| Number | Source (verified) |
|---|---|
| **34 s** cold build of the metadata-results set | Owner observation 2026-08-05, recorded in the fragment |
| **21.9 s** for the same build | In-code comment, `internal/server/metadata_results_cache.go:24` (PR #2142) |
| **36,805** result rows in that build | `internal/server/metadata_results_cache.go:25`; repeated at `internal/server/metadata_results_cache_test.go:32` |
| **60 s** memo TTL | `metadataResultsCacheTTL`, `internal/server/metadata_results_cache.go:20` |
| **5000** operations pulled per build | `store.GetRecentOperations(5000)`, `internal/server/metadata_batch_candidates.go:796` |
| **6,104 ms** cold vs **105 ms** warm, ABS contributor build | `internal/server/wire_abs_routes.go:260` |
| **44,888** books walked per contributor rebuild | `internal/server/handlers/abs/browse.go:518` |
| up to **93** consecutive contributor page requests from one client action | `internal/server/handlers/abs/browse.go:509`, `:514` |
| **~9,200** authors | `internal/server/handlers/abs/browse.go:514` |
| **5 min** ABS contributor TTL | `absAuthorsCacheTTL`, `internal/server/handlers/abs/browse.go:504` |
| **~2.5 min** memdb warmup on this library | `internal/server/library_list_warmer.go:211-212` |

The 34 s and 21.9 s figures are two observations of the same build against a growing
operations keyspace at different times. Neither supersedes the other, and nothing in
this design depends on which is right — only on both being "many seconds", which they
are.

### 1.2 What is cold, and for how long

`GET /api/v1/library/metadata-results` is routed at
`internal/server/server_lifecycle.go:1423` to `handleListMetadataResults`
(`internal/server/metadata_batch_candidates.go:844`), which reads through
`latestMetadataResultsByBookCached` (`internal/server/metadata_results_cache.go:55`).
That memoises `latestMetadataResultsByBook`
(`internal/server/metadata_batch_candidates.go:795`) for `metadataResultsCacheTTL`.

**Nothing pre-populates the memo.** `startCacheWarmers`
(`internal/server/server_lifecycle.go:718`, called from `:308`) launches exactly five
warmers plus one sweep — `facets-warmer` (`:720`), `library-sizes-warmer` (`:733`),
`library-list-warmer` (`:751`), `authors-warmer` (`:757`), `series-warmer` (`:766`),
`apikey-expiry-sweep` (`:777`). There is no metadata-results warmer. Grep confirms the
only non-test caller of `latestMetadataResultsByBookCached` is the one HTTP handler at
`metadata_batch_candidates.go:857`.

So after every restart, the first person into the match UI pays the full build.

**A boot warm alone is not the fix.** With a 60 s TTL it covers the first 60 seconds of
uptime. Anyone arriving at minute 5 pays the same 34 s the owner is complaining about.
The fragment asks for a warmer; the warmer is necessary and insufficient.

### 1.3 The invalidate cliff hits exactly the user this workstream serves

`invalidateMetadataResultsCache` (`internal/server/metadata_results_cache.go:86`) sets
`latest` and `counts` to `nil`. It is deferred from three write paths in
`internal/server/metadata_batch_candidates.go` — lines **478**, **583**, **647**
(`handleBatchApplyCandidates` at `:475` and its reject / unreject siblings).

Consequence: the user approves a batch of matches and their **very next** page load
hits a fully-empty cache and blocks for the whole build. The person picking matches —
the exact person the memo was added for — is the one guaranteed to eat the cold path.
Boot-warming does nothing about this.

### 1.4 The twin: authors + narrators on first paint

The fragment pairs this item with "authors/narrators failing to load on first paint".
The verified picture:

- **The internal entity endpoints are not the problem.** `entities.Handler.ListAuthors`
  (`internal/server/handlers/entities/handler.go:305`) is backed by `s.authorsCache`
  (declared `internal/server/server.go:186`, constructed `:410` with a 24 h TTL) **and**
  already has a boot warmer (`warmAuthorsCache`,
  `internal/server/entity_cache_warmers.go:15`, enrolled as `authors-warmer` at
  `server_lifecycle.go:757`). `ListNarrators`
  (`internal/server/handlers/entities/handler.go:1077`) and `CountNarrators` (`:1091`)
  are uncached, but they reach `PebbleStore.ListNarrators`
  (`internal/database/pebble_store_authors.go:774`), a single `narrator:` prefix scan —
  cheap. **There is no `narratorsCache` field on `Server`**; grep of
  `internal/server/server.go` confirms only `authorsCache` (`:186`) and `seriesCache`
  (`:187`) exist. It does not need one.
- **The ABS surface is the problem**, and it is the only build in the repo producing
  authors *and* narrators together: `contributorsCached`
  (`internal/server/handlers/abs/browse.go:525`) returns
  `([]authorDTO, []narratorDTO, error)` off `h.authorsCache` / `h.narratorsCache` /
  `h.authorsCacheAt` (`internal/server/handlers/abs/handler.go:268-270`), TTL
  `absAuthorsCacheTTL` = 5 minutes (`browse.go:504`). Its warm at
  `internal/server/wire_abs_routes.go:266-271` is a **bare `go func()`**: grep for
  `bgWG` in `wire_abs_routes.go` returns nothing, making it the only cache warmer in
  the server not enrolled in the shutdown wait-group. It first blocks on
  `ps.WaitForWarmup()` (`internal/database/pebble_store.go:153`), ~2.5 min on this
  library.

So the ABS Authors/Narrators tab is cold for the first ~2.5 minutes after every restart
and cold again every 5 minutes thereafter. Same pattern, same fix.

**A hypothesis that was checked and is wrong — recorded so nobody re-derives it.** The
`authors-warmer` / `series-warmer` do **not** need a `WaitForWarmup` gate.
`WaitForWarmup`'s own doc (`internal/database/pebble_store.go:145-152`) states memdb
publication is atomic and reads fall back to Pebble until it publishes;
`GetAllAuthorBookCounts` (`internal/database/pebble_store_authors.go:491-494`)
therefore reads either a fully published memdb or a **complete** Pebble scan
(`:495`, "Full Pebble book scan combined with junction table scan"). Warming pre-memdb
is slower, never wrong. `AuthorSeriesService.ListAuthorsWithCounts`
(`internal/audiobooks/author_series.go:77`) does call `GetAllAuthorBookCounts`
(`:86`) — verified, the chain is real — but the *conclusion* about it was not. No
change is proposed there.

---

## 2. Goal

Nobody waits on a metadata-results or ABS contributor build, whether they arrive at
second 1 or minute 40, and whether or not they just approved a batch.

**Non-goal:** making the build fast enough that caching is unnecessary. It will still
take seconds. This design makes the wait never fall on a request.

---

## 3. Locked decisions

### D1 — Boot warmer, enrolled exactly like the other five

New `(*Server).warmMetadataResultsCache()` calling
`latestMetadataResultsByBookCached(s.Store())` and logging the outcome, registered in
`startCacheWarmers` (`internal/server/server_lifecycle.go:718`) as
`metadata-results-warmer` with the identical shape the five existing warmers use:

```go
s.bgWG.Add("metadata-results-warmer")
go func() {
    defer s.bgWG.Done("metadata-results-warmer")
    defer warmerRecover("metadata-results")
    if s.bgCtx.Err() != nil {
        return // server already shutting down — skip, never warm a closing store
    }
    s.warmMetadataResultsCache()
}()
```

`s.bgWG` is `namedWaitGroup` (`internal/server/server.go:266`, defined in
`internal/server/bg_wg.go:24`); `warmerRecover` is
`internal/server/server_lifecycle.go:712`; `s.Store()` is
`internal/server/server.go:313`.

**WHY the bgWG + bgCtx pair is non-optional.** `server_lifecycle.go:699-706` records
that fire-and-forget warmers outliving `Store().Close()` panicked with `pebble: closed`
(the PEBBLE-CLOSED family, #1781/#1794). A new warmer that skips enrolment reopens
that. `internal/server/cache_warmers_bgwg_test.go:54` / `:86` already assert on this
shape.

**WHY it does NOT wait for memdb.** Unlike `warmAudiobookListCache`
(`internal/server/library_list_warmer.go:196`, which gates on `memReadyChecker`,
declared `:145`, asserted `:204`) and unlike the ABS warm, this build reads only
`operation:` and `op_result:` keys straight from Pebble — no memdb path exists for
either (`PebbleStore.GetRecentOperations`,
`internal/database/pebble_store_operations.go:61`; `PebbleStore.GetOperationResults`,
`:479`). Gating on a ~2.5 min memdb warmup would delay it for no correctness gain. The
`unfetched` bucket *does* use `store.ListBookIDs()`
(`internal/database/iface_book.go:50`), but that lives in the **handler**
(`internal/server/metadata_batch_candidates.go:871`), outside the cached build, and is
not warmed (see §8 Q2).

**WHY its own file, not `entity_cache_warmers.go`.** That file's header
(`internal/server/entity_cache_warmers.go:6-9`) declares its scope as "the non-HTTP
author/series cache warmers" explicitly. The metadata-results warmer belongs in
`internal/server/metadata_results_cache.go`, next to the cache it warms.

### D2 — Stale-while-revalidate, because a boot warm covers 60 seconds

This is the decision that makes the fragment's ask actually work.

`latestMetadataResultsByBookCached` gains three states instead of two:

| State | Condition | Behaviour |
|---|---|---|
| **fresh** | entry present, age < `metadataResultsCacheTTL` (60 s) | serve, no rebuild |
| **stale** | entry present, 60 s ≤ age < `metadataResultsStaleMax` | **serve immediately**, kick at most one background rebuild |
| **cold** | no entry, or age ≥ `metadataResultsStaleMax` | block on the rebuild (today's behaviour) |

- `metadataResultsStaleMax = 15 * time.Minute`. **WHY a hard bound at all:** without
  one, a rebuild that fails forever would keep serving a set of unbounded age while
  looking authoritative. With it, a wedged rebuild degrades to slow-or-erroring, which
  is honest, instead of silently ancient, which is not.
- At most one background rebuild in flight, guarded by a new `inflight bool` field
  inside the existing `metadataResultsCache` struct
  (`internal/server/metadata_results_cache.go:38`) under the existing `mu`. **Not
  `singleflight`** — one bool is enough and adds no dependency.
- **The background rebuild goroutine MUST carry its own `recover()`.** Reuse
  `warmerRecover("metadata-results-refresh")`. Today's
  `TestLatestMetadataResultsByBookCached_ServesAFreshEntryWithoutRebuilding`
  (`internal/server/metadata_results_cache_test.go:35`) passes a **nil store** and
  proves no rebuild happened by relying on a build panicking. That specific test primes
  a *fresh* entry, so it stays green under SWR — but the pattern is established in the
  file, and the moment anyone primes an *expired* entry and calls the cached function
  with a nil store, an unrecovered panic in a detached goroutine takes the process
  down. The recover is what turns that into a logged warning instead of a crash.
- The response gains `cache_age_seconds` so the UI can say "as of 40 s ago" rather than
  implying live data. **Stale must be labelled stale, never disguised.**

### D3 — Stale, not nil, on apply / reject / unreject

The three `defer invalidateMetadataResultsCache()` call sites
(`internal/server/metadata_batch_candidates.go:478`, `:583`, `:647`) switch to a new
`markMetadataResultsCacheStale()` that **keeps the map** and back-dates
`metaResultsCache.at` past the TTL, so the next read takes the D2 **stale** branch:
serve instantly, refresh in the background.

**WHY this is honest rather than a lie.** `PebbleStore.CreateOperationResult`
(`internal/database/pebble_store_operations.go:469-477`) writes key
`op_result:<OperationID>:<BookID>`. The apply path
(`internal/server/metadata_batch_candidates.go:540`), reject (`:622`) and unreject
(`:686`) all write with the *same* `OperationID` and `BookID` they read, so they
**overwrite the row in place**. The stale set therefore differs from truth by exactly
the statuses of the books the user just acted on, for exactly one refresh cycle — and
the response labels its own age via `cache_age_seconds`. That is strictly better than
today's behaviour, which blocks that same user for 34 s.

**Rejected alternative, and what it would cost.** A copy-on-write patch — clone
`latest`, set `Status` on the named `req.BookIDs`, recompute `counts`, install the
clone — would reflect the change immediately with no stale window. It is correct and
implementable (the handlers know both the IDs and the target status). It is **not**
taken here because it is a new mechanism with its own concurrency surface, and this
workstream is scoped to the cold start. Recorded as §8 Q1 for a follow-up.

`invalidateMetadataResultsCache()` is **kept unchanged** as the nil-everything escape
hatch: `resetMetadataResultsCache` (`internal/server/metadata_results_cache_test.go:17`)
and `TestInvalidateMetadataResultsCache_ClearsTheEntry` (`:75`) both depend on
nil-everything semantics, and a genuine "I have no idea what changed" caller should
still exist.

### D4 — Same two fixes for the ABS contributor warm

1. **Enrol the warm** at `internal/server/wire_abs_routes.go:266-271` in `s.bgWG` as
   `abs-contributors-warmer`, with `warmerRecover("abs-contributors")` and a `s.bgCtx`
   skip — the same protection every other warmer received in #1781/#1794 and which this
   one alone lacks. `wireABSRoutes` is a `*Server` method
   (`internal/server/wire_abs_routes.go:99`), so `s.bgWG` and `s.bgCtx` are in scope.

   **The `bgCtx` check must appear TWICE — before AND after `WaitForWarmup()`:**

   ```go
   s.bgWG.Add("abs-contributors-warmer")
   go func() {
       defer s.bgWG.Done("abs-contributors-warmer")
       defer warmerRecover("abs-contributors")
       if s.bgCtx.Err() != nil { return }
       if ps, ok := s.Store().(*database.PebbleStore); ok {
           ps.WaitForWarmup()
       }
       if s.bgCtx.Err() != nil { return } // shutdown arrived during the ~2.5 min wait
       handler.WarmContributors(context.Background())
   }()
   ```

   **WHY not pass `s.bgCtx` into `WarmContributors` instead.** That looks tidier and is
   wrong. `WaitForWarmup` (`internal/database/pebble_store.go:153`) takes **no context**
   — it is a bare `<-p.warmupDone` — so a warmer blocked in it cannot observe
   cancellation at all. `Shutdown` cancels `bgCtx` and *then* calls `s.bgWG.Wait()`
   (`internal/server/server_lifecycle.go:576`). Enrolling this goroutine therefore makes
   `Wait()` block for the remainder of the ~2.5 min warmup no matter what context
   `WarmContributors` receives. The second `bgCtx` check is what actually matters: it
   stops a **6-second full-library scan against a store that is about to close**, which
   is the real PEBBLE-CLOSED exposure D4 exists to remove. Threading a cancellable
   context into the build would additionally change `WarmContributors`'s semantics
   mid-build for no gain.
2. **Give `contributorsCached`** (`internal/server/handlers/abs/browse.go:525`) the same
   stale-while-revalidate treatment as D2, so the 5-minute TTL stops producing one cold
   request every 5 minutes. State lives on the existing `h.authorsCacheMu` /
   `h.authorsCache` / `h.narratorsCache` / `h.authorsCacheAt`
   (`internal/server/handlers/abs/handler.go:267-270`); time comes from the existing
   injected `h.now` field (`internal/server/handlers/abs/handler.go:273`), so tests can
   drive the clock without sleeping.

**The `WaitForWarmup()` gate stays.** That one *is* load-bearing, for the reason stated
verbatim at `internal/server/wire_abs_routes.go:262-265`: the contributor set is derived
from *visible* books (`contributorDTOs` → `h.visibleBookSummaries`,
`internal/server/handlers/abs/browse.go:613-615`), so building it pre-memdb would cache
a view of a library that does not exist yet and then serve it for the whole TTL.

---

## 4. Data model / API

**No schema change. No store-interface change. No new operation, no new op ID.**

`GET /api/v1/library/metadata-results` gains one additive field alongside the existing
`items` / `total` / `by_status` / `limit` / `offset` (built at
`internal/server/metadata_batch_candidates.go:932-938`; typed in TS as
`MetadataResultsResponse`, `web/src/services/api.ts:3580-3586`):

| Field | Type | Meaning |
|---|---|---|
| `cache_age_seconds` | number | age in seconds of the served build; `0` when the build was performed for this request |

Additive only: the TS interface gains one optional field and no existing caller
changes. `getMetadataResults` (`web/src/services/api.ts:3597`) needs no edit to keep
working.

---

## 5. Failure modes

| # | Failure | Handling |
|---|---|---|
| F1 | Background refresh goroutine panics | `warmerRecover("metadata-results-refresh")` inside it; the stale entry survives, the next request re-kicks |
| F2 | Rebuild fails forever (store error) | stale served until `metadataResultsStaleMax` (15 min), then requests block and surface the real error rather than serving something ancient |
| F3 | Refresh stampede — N concurrent requests each spawn a rebuild | `inflight` bool under the existing `mu`; at most one in flight |
| F4 | Warmer outlives `Store().Close()` → `pebble: closed` panic | `bgWG` enrolment + `bgCtx` skip, exactly as `internal/server/server_lifecycle.go:718-774` does for the other five (D1, D4) |
| F5 | ABS warm outlives shutdown | closed by D4 step 1. **Residual, stated so it is not a surprise:** because `WaitForWarmup` is uncancellable, `bgWG.Wait()` can still block for the remainder of the ~2.5 min memdb warmup on a shutdown issued during boot. What D4 removes is the 6-second scan against a closing store, not the wait itself |
| F6 | User acts on a batch, then reads a stale list | one refresh cycle only, and the response carries `cache_age_seconds` so the staleness is visible, not disguised (D3) |
| F7 | Boot warmer competes with memdb warmup for disk IO | accepted: it is one build, and the alternative (gating on ~2.5 min memdb) leaves the endpoint cold *longer* than today |
| F8 | Read error inside the build silently drops books | **not fixed here** — inherited known defect, §7.1. Named so it is not mistaken for something this design closed |
| F9 | >5000 mixed-type operations push older fetch ops out of the window | **not fixed here** — inherited known defect, §7.2 |
| F10 | Parallelised build loses or reorders rows | not applicable — the build is not parallelised here (§7.3) |

**No write-back-wipe surface.** Nothing in this design calls `UpdateBook` or
`UpdateBookFile`; neither symbol appears in any file it touches. The dominant incident
class is structurally absent here, not merely avoided.

---

## 6. Explicit non-goals

- **No apply path.** Nothing is written, moved, merged or deleted. The dry-run/apply
  gate that governs the other 2026-08-05 workstreams has no subject here; §9 defines
  what stands in for it.
- **No `books/itunes/**` access, and no filesystem access at all.** This design reads
  `operation:`, `op_result:`, and (via the ABS path) author / narrator / book keys.
- Not making the build fast enough to drop the cache.
- **Not parallelising `latestMetadataResultsByBook`** — see §7.3 for why this is a
  documented defect rather than in-scope work.
- Not adding a narrators cache to the internal entities handler (§1.4: single cheap
  prefix scan).
- Not changing the values of `absAuthorsCacheTTL` or `metadataResultsCacheTTL`. SWR
  removes the need to tune them.
- Not touching `authors-warmer` / `series-warmer` (§1.4 correction: no bug there).
- No frontend redesign. One optional response field; consuming it is optional.

---

## 7. Known defects found while verifying — NOT fixed here

Each carries its evidence so a follow-up does not have to re-derive it.

### 7.1 A read error is laundered into "never fetched"

`internal/server/metadata_batch_candidates.go:810-813`:

```go
results, err := store.GetOperationResults(op.ID)
if err != nil {
    continue
}
```

A failed read silently drops every book in that operation from `latest`. Those books
then appear in the response as **`unfetched`**, because `handleListMetadataResults`
derives that bucket by diffing `store.ListBookIDs()` against `latest`
(`internal/server/metadata_batch_candidates.go:871-880`). A read error is rendered to
the user as "this book has never been fetched".

That is *absent evidence presented as negative evidence*, in the one endpoint whose
entire job is reporting what has and has not been fetched. Nothing counts or logs it
today. A fix would WARN-log per failure with the op ID, carry a `result_read_errors`
count on the rebuild log line and in the response, and set an
`unfetched_is_exact: false` flag whenever that count is non-zero so callers know the
`unfetched` number is a ceiling and not a measurement. **Out of scope: it is a
correctness fix to the build, not a cold-start fix.**

### 7.2 The 5000-operation window silently truncates history

`GetRecentOperations(5000)` (`internal/server/metadata_batch_candidates.go:796`) is
implemented at `internal/database/pebble_store_operations.go:61`: it scans the entire
`operation:` keyspace, unmarshals every record, sorts **all** of them by `CreatedAt`
descending (`:80-82`), and truncates to the limit (`:84-86`) — all *before* the caller
filters to `op.Type == "metadata_candidate_fetch"`
(`internal/server/metadata_batch_candidates.go:807`). Once the library accumulates more
than 5,000 mixed-type operations, older fetch operations fall out of the window and
their books silently become `unfetched`.

Fixing it needs a type-scoped operation lookup. **There is no `GetOperationsByType` on
`database.OperationStore`** — grep across the whole repo returns nothing, and
`internal/database/iface_ops.go:11-60` has no such method. Adding one is a
store-interface change, which §4 rules out for this workstream.

### 7.3 The build is a serial per-operation DB fan-out

`latestMetadataResultsByBook` (`internal/server/metadata_batch_candidates.go:795-829`)
opens one fresh Pebble prefix iterator per operation via `GetOperationResults`
(`internal/database/pebble_store_operations.go:479`) — up to 5,000 iterator opens plus
36,805 JSON unmarshals, **serially, on one core**.

`CLAUDE.md` § "Concurrency — Prefer Multi-Core Design (MANDATORY)" describes this shape
("a loop over a large collection doing a DB read per item"). It is a **pre-existing**
loop, not one this workstream introduces, and under D2 no request ever waits on it. If
it is parallelised later, the correct tool is
`errgroup.Group` + `SetLimit(runtime.NumCPU())` (`golang.org/x/sync v0.22.0` is already
at `go.mod:39`), **not** `registry.RunItems`
(`internal/operations/registry/run_items.go:82`) — that takes a `registry.Reporter`, and
there is no operation and no reporter on a warmer or HTTP path.

A parallel version **must** add a deterministic tiebreak: today the winner is whichever
result has the later `CreatedAt` with a strict `.After()`
(`internal/server/metadata_batch_candidates.go:816-819`), so ties resolve by iteration
order, which `GetRecentOperations`'s descending sort makes stable and which per-worker
merging would make **non**-deterministic. Tiebreak on `OperationID` lexicographically,
**not** on `OperationResult.ID`: that field exists
(`internal/database/store.go:510`) but `CreateOperationResult`
(`internal/database/pebble_store_operations.go:469-477`) never assigns it and callers
construct the struct without it (e.g. `internal/server/metadata_ops.go:290`, `:379`,
`:700`, `:767`). It is always `0` on this backend.

---

## 8. Open questions

1. **Should apply/reject/unreject patch the cache instead of marking it stale?** D3
   takes the smaller option. The copy-on-write patch (clone `latest`, set `Status` for
   `req.BookIDs`, recompute `counts`) would remove even the one-cycle stale window.
   Proposed: revisit only if that one cycle is actually noticed in use.
2. **Should the boot warmer also pre-warm the `unfetched` diff?** It needs
   `store.ListBookIDs()` and therefore memdb. Proposed: **no** — it is a handler-side
   concern reached only with `include_unfetched=true`, and warming it would drag a
   memdb dependency into a warmer that currently has none (D1).
3. **Should `metadataResultsStaleMax` be configurable?** Proposed: no, a const, until
   something demands otherwise.
4. **Does the ABS client tolerate a stale contributor list across a 93-page sequence?**
   `absAuthorsCacheTTL`'s comment (`internal/server/handlers/abs/browse.go:499-503`)
   records that the owner already accepted a 5-minute-stale list, so SWR at the same
   bound should be safe — worth one confirmation before shipping D4.

---

## 9. What stands in for a dry-run gate

There is no apply, so the gate is observational. The instrument already exists —
`internal/server/metadata_results_cache.go:71-72` logs:

```
metadata-results cache rebuilt   books=<n> duration_ms=<ms>
```

Adding a `trigger` field (`boot` | `demand` | `refresh`) makes the gate checkable from
logs alone. The ABS side has its twin at
`internal/server/handlers/abs/browse.go:1015` (`abs: contributor cache warmed`, with
`duration_ms`). The exact numbers that must be observed before this is called working
are in the plan's **Acceptance gate** section.
