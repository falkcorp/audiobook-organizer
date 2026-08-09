<!-- file: docs/design/2026-08-09-search-backend-options.md -->
<!-- version: 2.1.0 -->
<!-- guid: 4f1c8a72-6d93-4e05-b8a1-9c72e0f45d38 -->
<!-- last-edited: 2026-08-09 -->

# Search backend — design options and trade-offs

**Status:** decision document, and the intended basis for a later spec. **Two decisions
taken — see §0a. Sorting cost analysed in §0b**, which answers the "will Go be slow/bloated"
question and ends with the three questions a spec needs settled. The rest remains open.
Written 2026-08-09 at the owner's request; updated the same day with the owner's answers
and the follow-up discussion on frontend-vs-backend sorting.
**Audience:** the owner, making a build-vs-buy-vs-keep call.
**Companion reading:** `todo.d/20260809-search-drops-filters-and-debounce.md`,
`docs/audits/2026-08-09-e2e-repair-and-ui-regressions.md` (findings 10 and 11).

---

## 0a. Owner decisions, 2026-08-09

Recorded verbatim in substance, because they close two of the ten questions in §6 and
change what the recommendation is:

> "I want the system to not suck and I want sorting replaced, and done by go."

**Decision 1 — sorting moves to the backend, in Go. Client-side sorting is to be
replaced, not supplemented.** This answers **Q9** and part of **Q3**.

**Decision 2 — "not suck" is the bar.** Taken as: the three defects in §2 are not
acceptable as permanent behaviour, and a fix that leaves search dropping filters or
firing per keystroke does not count as done. This resolves **Q3** as *all of the above*
rather than picking one of the five readings.

### What that means concretely, checked against the code rather than assumed

Sorting today is in three places, and only one of them is right:

| where | what it does | verdict |
|---|---|---|
| `internal/audiobooks/service_filtering.go:130` `applySorting` | Go, server-side, sorts the full filtered set | **Correct — keep and extend.** This is already "done by Go" |
| `web/src/components/common/ConfigurableTable.tsx:201` | `[...rows].sort(...)` on the client | **Broken by design — replace.** It sorts *the current page*, so on a paginated library it reorders the 50 rows you can see rather than the library. "Sort by title descending" gives you the wrong 50 books, correctly ordered among themselves |
| `web/src/services/api.ts` `searchBooksPage` | sends no `sort_by` at all | **Broken — fix.** Any search silently drops the sort order (§2.1) |

There are **15** client-side `.sort()` call sites in `web/src`. Most are legitimate —
sorting a book's own file list by track number, ordering tag clouds by count, ranking
metadata-search candidates by score. Those operate on complete, small, already-fetched
sets and are not what this decision is about. **The one that must go is any sort over a
paginated slice of the library**, because it is not a sort of the library at all.

Two further points that follow directly and are not optional if the bar is "not suck":

1. **A sort is only meaningful if it is applied before pagination.** This is the same
   defect as §2.2 (filters applied after pagination) and wants the same fix. Sorting
   server-side without also pushing the filters down would produce a correctly-sorted
   page of the wrong rows.
2. **There is currently no UI control to sort at all** (audit finding 1: `SearchBarProps`
   has no `onSortChange`; `LibraryBookGrid.tsx:133` takes the handler as
   `_handleSortChange`, underscore-prefixed to mark it unused). So "replace sorting" is
   partly "build the control that was removed", not only "move the logic". The state and
   URL plumbing still work end-to-end — only the affordance is missing.

### Revised recommendation

§7's order still holds, with sorting folded in rather than bolted on afterwards:

1. **Fix §2.1 and §2.3** — client sends filters *and* `sort_by`; debounce the box. Hours.
2. **Push filters AND sort into the Bleve query (Option A1)**, so both are applied before
   pagination. This is the single piece of real engineering and it now covers sorting.
3. **Delete the client-side library sort** (`ConfigurableTable` row sort, where it is used
   for the library grid) and restore the sort control wired to the existing URL/state
   plumbing.
4. Re-evaluate the engine question only after that.

**Not changed by these decisions:** the engine choice (§4) and the API-shape choice (§5).
Sorting in Go is orthogonal to whether the engine stays Bleve, and orthogonal to GraphQL.

---

## 0b. Sorting cost: will a Go backend be slow, or bloated?

Owner's concern, 2026-08-09:

> "My big concern is how slow the go backend alone will be to respond without loading
> everything into ram and making audiobook organizer a huge bloated mess."

It is the right question, and the code gives it a sharper answer than expected:
**that has already happened.** This is not a risk to avoid; it is the current design, and
it was paid for deliberately.

### The backend already holds the whole library in RAM

`internal/database/memdb_*` warms an immutable radix tree at boot. From
`memdb_strip.go:14`:

> Memory math (392K-book production library) … Description avg ~500-2000 chars →
> ~400MB-1.5GB across all books … **Stripping these from memdb cuts the radix tree's
> resident size from ~10GB to ~2GB.**

So someone already hit exactly this wall and paid for it by stripping `Description`,
fingerprint blobs and `VersionNotes` out of the resident copy. The question is therefore
not *"should Go load everything into RAM?"* — it already does — but **"given that, why is
sorting still expensive?"**

### Sorting splits in two, and only one half is the problem

**Title sort — already cheap.** memdb's title index *is* a sorted radix tree, so the walker
streams and stops at `limit+offset`. Nothing is materialised.

**Every other sort — this is the cost.** `service_query.go:155`:

```go
pdLimit, pdOffset := limit, offset
if heavySorting {
    pdLimit, pdOffset = 0, 0   // fetch the FULL filtered set
}
```

then `applySorting(books, f)` and paginate afterwards. Sorting by author, duration or date
added **materialises the entire filtered set into a slice, sorts it, and discards all but
50 rows.** That is the bloat, and it ships today.

Note what the code is doing there: it disables pagination *to make sorting correct*. A sort
applied after pagination is not slow, it is **wrong** — it orders the 50 rows you already
have. The current code chose slow over wrong, which was the right call, and the fix is to
stop having to choose.

### So the lever is indexes, not memory

A secondary sorted index stores **keys and IDs, not books**. Rough order of magnitude: a
short key plus an ID plus tree overhead across the library is on the order of **tens of MB
per sort field**, against a **2GB** resident tree. Five sortable fields is a low-single-digit
percentage increase, and each one converts a full-set materialise-and-sort into the same
streaming walk that title already enjoys.

That is the trade to weigh: a small, bounded, *predictable* increase in resident memory in
exchange for deleting an unbounded per-request allocation.

**Alternative — let Bleve sort.** It already indexes these fields with `Store: true` and
supports sort-by-field, so search + filter + sort + paginate would happen in one place
instead of three. Conceptually cleaner and it fixes §2.2 at the same time; more work, and
it promotes the index from a relevance dependency to a **correctness** dependency, which
raises the stakes on the staleness question (Q8).

### ⚠️ Settle the scale numbers before designing

The figures in the code disagree, and the RAM-per-index estimate moves ~6× depending on
which is right:

| source | figure |
|---|---|
| `memdb_strip.go:14`, `memdb_warmup.go:61` | **392K-book** production library |
| `pebble_store.go:689`, `memdb_reads.go:585` | **~68K** books scanned unfiltered |
| `memdb_summaries.go:18` | **~38K** primary rows |

These are probably counting different things (all book rows vs primary versions vs
book_files), or one is stale. **Resolve this first** — it is the single input that most
changes the design.

### Where this leaves the recommendation

Unchanged in order, sharper in content:

1. Client sends filters and `sort_by`; debounce the box (§2.1, §2.3). Hours.
2. **Push filters AND sort into the query so both are applied before pagination.** This is
   the one piece of real engineering, and it is what removes the full-set materialise.
3. Delete the client-side library sort and restore the sort control.

The frontend keeps what it should: tracklists, tag clouds, metadata candidates — complete
small sets it already holds. The rule stays *paginated slice → server; complete set in hand
→ client.* "Sorting done by Go" should mean **Go never builds that slice at all**, not "Go
sorts a 38K-row slice per request."

### Open questions for the spec

- **Secondary sorted indexes, or Bleve does the sorting?** Both remove the materialise;
  they differ in blast radius and in how much they couple correctness to the index.
- **Which sort fields actually matter?** Every one costs index memory. "All of them" is the
  expensive answer, and probably nobody sorts by publisher.
- **Which scale figure is real?** See the table above.

---

## 0. Read this first: the premise of the question is probably wrong

The request was "all the designs we could use for the search backend." Before the menu,
the single most important finding from grounding this doc in the actual code:

> **You already have a real search engine, fully wired, and the user-visible search
> problems are almost entirely in the client and in one pagination-ordering bug — not in
> the engine.**

`internal/search/` contains a complete Bleve (scorch) integration: a hand-written query
DSL with a lexer, an AST, a Bleve translator, facet counts, highlighting, per-user filter
support, and an index builder. It is opened at
`{dirname(config.DatabasePath)}/library.bleve` (`internal/search/register.go`) and routed
into the list path at `internal/server/server_lifecycle.go:298`.

**Verified on the production host, not assumed** (2026-08-09): the index file exists in
the configured data directory, and the current process logged

```
level=INFO msg="Search index opened" path=<datadir>/library.bleve
```

on the run started by this morning's deploy — and on all four prior restarts going back to
Aug 07. **Bleve is live in production.** The silent-fallback scenario (§6 Q1) is ruled out
at the "is it open" level. What is *not* yet verified is whether the index is
**complete and current** — see Q1 for how to check that, which needs either elevated file
access or a working API token (the checked-in `.api-token` returns
`{"error":"invalid session"}` and needs regenerating).

So the honest framing of the decision is not "which search backend should we build."
It is:

1. ~~Is Bleve actually running?~~ **Answered: yes.** Remaining sub-question: is the index
   complete? (Q1)
2. **Do we fix the three defects sitting on top of it** (§2), which is days of work?
3. **Or do we replace the engine** (§4), which is weeks and only pays off if the answer to
   specific questions in §6 is yes?

Replacing the engine before fixing §2 would carry every current symptom straight into the
new system, and would then be blamed on the new system.

---

## 1. What exists today, precisely

### 1.1 Two search paths, one is a fallback

`internal/audiobooks/service_query.go:74-79`:

```go
if search != "" {
    if svc.searchIndex != nil {
        books, err = svc.searchWithBleve(search, limit, offset, f.UserID)
    } else {
        books, err = svc.store.SearchBooks(search, limit, offset)
    }
}
```

**Path A — Bleve.** `searchWithBleve` parses the query into an AST, translates to a Bleve
query, and executes. On *either* a parse error or a translate error it silently falls back
to Path B. That fallback is deliberate and documented (punctuation-heavy titles the DSL
rejects), but it means a user can get dramatically worse results from a query that merely
*looks* slightly wrong, with no signal that it happened.

**Path B — `PebbleStore.SearchBooks` (`internal/database/pebble_store.go:2380`).** A full
`book:*` iterator scan. For every book: JSON-unmarshal, then
`strings.Contains(strings.ToLower(title), q)` plus the same on a pre-loaded author-name map
and on narrator. No index, no ranking, no relevance — results come out in key order.
It is O(N) per keystroke, and there is no client debounce (§2.3), so it is O(N) *per
character typed*.

### 1.2 What Bleve indexes

From `internal/search/bleve_index.go:270+`. Analyzed with the stock English analyzer
(lowercase, stop words, stemming, ascii-folding): `title`, `author`, `series`, `narrator`,
`publisher`, `description`, `file_path`. Plus keyword (exact), numeric and boolean field
mappings. Query-time field boosts live in `textFieldBoosts`; Bleve v2 has no index-time
field boost, and the code comments correctly note this.

### 1.3 The DSL that already exists

`internal/search/query_ast.go` documents the user-facing syntax:

| Feature | Syntax |
|---|---|
| AND | whitespace, `&&`, `AND` |
| OR | `\|\|`, `OR` |
| NOT | `-` prefix, `NOT` |
| Grouping | `(…)` |
| Within-field alternation | `field:(a\|b\|c)` |
| Numeric ranges | `field:>N`, `field:<N`, `field:[A TO B]` |
| Prefix / wildcard | `field:vamp*` |
| Fuzzy | `field:smith~` |
| Boost | `field:vampire^3` |

This is a genuinely capable query language and it is **already built and tested**
(`query_parser_test.go`, `query_parser_prop_test.go` — there is even a property test).
Any replacement engine has to match this surface or break the sidebar filters, which
encode filters as DSL strings like `read_status:in_progress`.

### 1.4 The non-search list path is sophisticated

Worth knowing before proposing to replace anything: when `search == ""`, the list path has
filter pushdown into the memdb walker, a results cache with a carefully-built key, and a
separate "heavy filter" pushdown that avoids materialising the corpus
(`service_query.go:110-200`). Comments there record two previously-shipped bugs — a
pointer formatted with `%v` making the cache key unique per request, and a double-pagination
bug that returned zero rows for every page 2. This code has been debugged into shape.
**A new engine would have to re-earn that.**

---

## 2. The three real defects (fix these before choosing an engine)

### 2.1 The client drops every filter when you type — client-side, ~1 hour

`web/src/services/api.ts:1023-1037`, verified today:

```ts
const params = new URLSearchParams({
  search: query,
  limit: String(limit),
  offset: String(offset),
  is_primary_version: 'true',
});
if (showFailed) params.set('show_quarantined', 'true');
```

No `library_state`, no `filters`, no `tags`, no `sort_by`. `useLibraryQuery.ts:192-193`
routes any non-empty search through this function.

**The backend is not the problem.** `service_query.go:226` runs the full post-filter block —
tags, library state, field filters, per-user filters — on the search path too, because
`hasPostFilters` is computed from the filter struct at line 53 and only zeroed inside the
*pushdown* branches. The server would honour these parameters today if the client sent them.

### 2.2 Post-filtering happens after pagination — server-side, and this one is architectural

This is the defect that actually argues for a design change, and it is subtle.

`searchWithBleve` calls `svc.searchIndex.SearchNative(bleveQ, offset, limit)`
(`service_query.go:658`) — Bleve paginates. The post-filter block at line 226 then removes
rows *from that already-paginated page*. Consequences:

- A page of 50 can return 9 rows. The UI shows a short page and the pagination control lies.
- Total counts are wrong whenever a filter is combined with a search.
- "Next page" skips rows, because the offset is in pre-filter space.

The codebase already knows about this and works around it in exactly one place: the
per-user-filter path over-fetches to `searchPostFilterWindow = 10000`
(`service_query.go:566`) and filters within that window. That is a bounded workaround for
one filter class, and the comment says as much.

**The clean fix is to push filters into the query rather than post-filter after it** —
which is precisely what a proper search backend is for, and which Bleve can already do via
conjunction queries. That is the strongest argument in this document for doing engine-level
work, and it does **not** require changing engines.

### 2.3 No debounce at all — client-side, ~30 minutes

Typing "Foundation" fires ten requests, one per keystroke. Measured in the e2e suite; the
test is literally named "search debounces input to avoid excessive requests" and asserts
`<= 3`. With Path B (§1.1) each of those is a full corpus scan.

**No engine choice fixes this.** Ten queries per search will embarrass any backend, and it
inflates every benchmark you might run to justify a migration.

---

## 3. Two axes, and conflating them is the classic mistake

The request mentioned GraphQL. GraphQL is **not a search backend** — it is an API shape.
These are independent choices and should be decided separately:

- **Axis A — the engine:** what actually matches, ranks and filters documents (§4).
- **Axis B — the API:** how the client asks for it and what comes back (§5).

Choosing GraphQL does not make search better, faster, or more relevant. It changes who
composes the query and how over-fetching is handled. It is worth evaluating on its own
merits, not as a search improvement.

---

## 4. Axis A — engine options

Constraints that shape all of this, taken from the repo rather than assumed:
PebbleDB is the only production DB; the app ships as a **single Go binary with an embedded
React frontend** (`//go:embed web/dist`); it is self-hosted on one box.

### Option A1 — Keep Bleve, fix the filter pushdown

**What:** Translate `library_state`, `tags`, and field filters into Bleve conjunction
clauses instead of post-filtering. Bleve returns correctly-paginated, correctly-counted
results. Delete the 10,000-row window workaround.

| Pros | Cons |
|---|---|
| Zero new dependencies, zero new processes | Requires indexing filterable fields that may not all be in the mapping today |
| Keeps the DSL, the tests, the facets, the highlighting | Index must stay in sync — a stale index becomes a *correctness* bug, not just a relevance one |
| Fixes §2.2 properly rather than widening the window | Bleve's community momentum is modest; you are somewhat on your own for exotic issues |
| Facets already implemented (`FacetCounts`) → sidebar counts become cheap | Scorch index adds disk and memory alongside Pebble |
| Single binary preserved | |

**Effort:** days. **Risk:** low-medium. **This is the default recommendation** unless §6 Q1
reveals Bleve is not actually running.

### Option A2 — Drop Bleve, hand-roll inverted indexes in Pebble

**What:** Maintain your own term → book-ID postings lists as Pebble keys.

| Pros | Cons |
|---|---|
| One storage engine, one backup, one consistency story | You are writing a search engine: stemming, ranking, phrase queries, fuzzy — all by hand |
| Full control over key layout and pushdown | The DSL's fuzzy/wildcard/boost features become months of work |
| No extra memory-mapped index files | Relevance ranking done badly is worse than substring matching, because it *looks* authoritative |
| Fits the existing "cached aggregates + dirty flag" idiom | Throws away working, tested code |

**Effort:** weeks-to-months. **Risk:** high. **Only sane if** the DSL surface shrinks
drastically (§6 Q4) and search means "exact field lookups," not "find me that book."

### Option A3 — SQLite + FTS5

**⚠️ This project has already considered and rejected SQLite, and the reasoning still
applies.** `docs/database-architecture.md` lists SQLite3 as "Opt-in, Legacy" and carries a
whole section titled **"Why Not SQLite3?"**, whose central objection is cross-compilation:
the SQLite C library must be built for each target. There is currently **no SQLite
dependency in `go.mod`** at all. I am including this option for completeness, not as a live
candidate — reopening it means reopening a settled architectural decision, and that needs a
better reason than "search."

**What it would give you:** FTS5 provides BM25 ranking, prefix and phrase search
in-process, and — the genuinely attractive part — filter combination as a plain SQL
`WHERE`, which fixes §2.2 by construction rather than by careful engineering.

| Pros | Cons |
|---|---|
| Extremely well-understood, huge operational track record | Second database to keep in sync with Pebble — the classic dual-write consistency problem |
| BM25 out of the box; SQL for filter combination, so §2.2 becomes a plain `WHERE` | cgo, or a pure-Go SQLite with its own trade-offs; complicates the single-binary story |
| Filters and search combine in one query plan — the right shape | FTS5 fuzzy support is weak vs. the existing DSL's `~` |
| Trivial to inspect and debug with a CLI | Rewrites the translator layer entirely |

**Effort:** weeks. **Risk:** medium. Genuinely attractive **if** filter-combination
correctness is the main pain, because SQL solves §2.2 by construction.

### Option A4 — External search service (Meilisearch / Typesense / OpenSearch)

| Pros | Cons |
|---|---|
| Best-in-class relevance, typo tolerance, faceting, instant-search ergonomics | **Breaks the single-binary deployment.** New process, new port, new backup, new failure mode |
| Meilisearch/Typesense are genuinely low-config | Sync pipeline Pebble → service, with reindex-on-drift handling |
| Handles §2.2 natively (filters are first-class) | OpenSearch is heavy for one box and one user |
| Offloads CPU from the app process | Another thing to upgrade, secure, and monitor — and `/metrics` is already down (see memory) |

**Effort:** weeks. **Risk:** medium-high, mostly operational. **Hard to justify** for a
self-hosted single-user library unless scale or relevance demands it (§6 Q2, Q3).

### Option A5 — Hybrid lexical + vector (semantic) search

**What:** Combine Bleve BM25 with embedding similarity. **The substrate already exists:**
`github.com/coder/hnsw v0.6.1` is in `go.mod`, local embeddings run via Ollama/bge-m3, and
there is an HNSW snapshot load path in the server lifecycle — all built for dedup.

| Pros | Cons |
|---|---|
| Answers "that book about the Roman general who becomes a slave" — lexical search cannot | Fusion ranking (RRF or similar) is a tuning problem with no obvious ground truth |
| Infrastructure largely paid for already | Embeddings must be maintained per book and re-generated on metadata change |
| Strong differentiator for a *personal library*, where you remember vibes not titles | Depends on Ollama being up — an external box (`windows-gpu`) |
| Reuses proven dedup embedding pipeline | Semantic results are hard to explain when they look wrong |

**Effort:** weeks, but incremental — it can sit *beside* A1 rather than replace it.
**Risk:** medium. **This is the only option that adds a capability rather than fixing a
defect,** and it is the one worth doing *after* A1, not instead of it.

### Engine summary

| | New deps | Single binary | Fixes §2.2 | Relevance | Effort | Risk |
|---|---|---|---|---|---|---|
| **A1** Bleve + pushdown | none | ✅ | ✅ | good | days | low-med |
| **A2** Pebble inverted index | none | ✅ | ✅ | poor unless heavily invested | months | high |
| **A3** SQLite FTS5 | one | ⚠️ cgo | ✅ by construction | good (BM25) | weeks | med — **and already rejected** |
| **A4** External service | process | ❌ | ✅ | best | weeks | med-high |
| **A5** Hybrid vector | mostly present | ✅ | inherits A1 | new capability | weeks | med |

---

## 5. Axis B — API shape (this is where GraphQL belongs)

### Option B1 — Keep REST, just send the parameters that already work

Fix §2.1: add `library_state`, `filters`, `tags`, `sort_by` to `searchBooksPage`.

| Pros | Cons |
|---|---|
| Roughly an hour; fixes the most-reported symptom | Query-string encoding of nested filters is already awkward and will get worse |
| No client or server architecture change | Doesn't address over-fetching of book payloads |
| Every existing test and mock keeps working | |

**This should happen regardless of every other decision in this document.**

### Option B2 — REST + a structured query body (`POST /audiobooks/query`)

| Pros | Cons |
|---|---|
| Nested boolean filters expressed as JSON, not URL-encoded soup | POST-for-read complicates HTTP caching and breaks bookmarkable URLs unless mirrored |
| Server-side validation with real types | Two list endpoints to keep consistent, or a migration |
| Natural home for filter pushdown (§2.2) | The URL is currently the source of truth for Library state — that plumbing works and is load-bearing |

### Option B3 — GraphQL

| Pros | Cons |
|---|---|
| Client picks exactly the fields it needs — real win, since library cards need ~8 fields and the API returns the whole book | **Does nothing for search quality.** The hard parts (§2.2, relevance) are unchanged |
| One endpoint for the Library's fan-out (books + facets + tags + counts) | N+1 resolvers are easy to write and this codebase has fought N+1/O(N²) repeatedly |
| Schema is self-documenting; strong typing to the TS client | Whole new server layer, new auth integration, new caching story, new tooling in the build |
| Filter composition is more natural than query strings | Query-cost limiting becomes *mandatory* — an unbounded query is a self-inflicted DoS |
| | Every existing e2e mock (`page.route` on REST paths) would need rewriting — that suite was just repaired at real cost |
| | Response envelope conventions (`body.data`, with `getBookTags`/`getBookExternalIDs` as exceptions) all change |

**Assessment, stated plainly:** GraphQL is a defensible answer to "the Library page makes
many REST calls and over-fetches fields." It is **not** an answer to "search is bad." If
the motivation is search, this is the wrong axis. If the motivation is the fan-out on page
load, it competes with B2 plus a couple of composite endpoints, at a fraction of the cost.

### Option B4 — Typed RPC (Connect / gRPC-Web / tRPC-style)

| Pros | Cons |
|---|---|
| End-to-end types without GraphQL's runtime machinery | Another codegen step in a build that already juggles Go + Vite + embed |
| Connect speaks HTTP/1.1+JSON — debuggable with curl | Doesn't solve field over-fetching the way GraphQL does |
| Good fit for a Go server with one first-party client | Same mock-rewrite cost as B3 |

---

## 6. Questions I need answered

Ordered by how much they change the recommendation. **Q1–Q3 are blocking** — I can't
responsibly recommend an engine without them.

### Blocking

**Q1. ~~Is Bleve running?~~ — ANSWERED YES. Remaining: is the index complete?**
I checked rather than asking. `<datadir>/library.bleve` exists and
`msg="Search index opened"` appears on the current process and on every restart back to
Aug 07. So the engine is live.

This mattered because `Open()` failures are downgraded to warnings so the server can boot
without search (`register.go`) — meaning a silent, indefinite fallback to the O(N)
substring scan was possible and nothing would have told you. **That scenario is ruled out.**

The residual question is **completeness**: an index that opens fine but is missing books
produces confidently wrong results. I could not measure it — the index directory is
root-owned (`sudo` requires interactive auth) and the checked-in `.api-token` is stale
(`{"error":"invalid session","code":"UNAUTHORIZED"}`). Two ways to settle it:

- `sudo du -sh <datadir>/library.bleve` and compare against the Pebble store — an index in the tens of MB for a library this size is plausible, single-digit MB
  is not.
- With a fresh token: search a term you know matches many books and compare the count
  against the same term through a filter-only path. A large gap means drift.

**Operational note worth acting on separately:** nothing alerts on the fallback. A startup
warning that scrolls past is the only signal. Given that `/metrics` is currently
unscraped (see the Prometheus memory), a silent search degradation could persist for
months. That is the same failure shape as the six e2e specs that sat disabled for four
months.

**Q2. How big is the library now, and what is the growth curve?**
Engine choice at 10K books and 500K books is not the same choice. §1.1's full-scan fallback
is tolerable at the low end and catastrophic at the high end.

**Q3. ~~What does "search is bad" mean?~~ — ANSWERED 2026-08-09: "not suck", i.e. all of
these, not a pick-one.** Left in full because the list is still the work breakdown:
- (a) "I filter to Organized, type an author, and get results from every state" → §2.1, one hour.
- (b) "It's slow / the UI stutters while typing" → §2.3, thirty minutes.
- (c) "Page 2 of a filtered search is wrong or empty" → §2.2, the architectural one.
- (d) "It doesn't find things I know are there" → relevance; needs A1 tuning or A5.
- (e) "I want to search by concept, not title" → A5, the only genuine new capability.

### Shapes the design

**Q4. Who uses the DSL syntax — you, or only the UI?**
The sidebar encodes filters as DSL strings (`read_status:in_progress`). If *you* also type
`series:foundation -narrator:smith` by hand, the DSL is a feature to preserve and A2/A3
get much more expensive. If it is purely an internal encoding, it could be replaced with a
structured filter object and the whole parser retired.

**Q5. Is the single-binary deployment a hard constraint?**
This is the make-or-break for A4. `//go:embed web/dist` and `make deploy` assume one
artifact. Is "one binary, one systemd unit" a requirement, or just how it happens to be?

**Q6. Should a search with zero results after filtering say so, or widen automatically?**
Bears directly on §2.2's fix: strict conjunction (correct, sometimes empty) vs. relaxing
filters with an explanation (friendlier, harder to reason about).

**Q7. Is multi-user real, or theoretical?**
There is a per-user filter path with a 10,000-row over-fetch window and a
`DisablePerUserSearchFilters` config flag. If this is effectively single-user, that whole
path can be simplified and §2.2 gets easier.

**Q8. What is the acceptable staleness between a metadata edit and search reflecting it?**
Currently there is an async index worker with a 1024-deep channel. Immediate consistency,
seconds, or minutes? Changes whether A1 can push filters into the index at all — if the
index can be stale, filter pushdown can return *wrong* results rather than merely
stale-ranked ones, which is a much higher bar.

**Q9. ~~Relevance ranking or deterministic ordering?~~ — PARTLY ANSWERED 2026-08-09:
sorting is to be replaced and done in Go (see §0a).** The open remainder is narrow: when a
user has NOT chosen a sort, should results come back in relevance order or a stable
default? The original note still stands and is the reason it matters:
Right now the fallback returns key order and Bleve returns score order — so **the ordering
silently changes depending on which path serviced the query.** That alone may explain some
"search is weird" reports.

**Q10. Budget in wall-clock time?**
"A weekend" and "a month" have genuinely different answers here, and I would rather aim at
the real number than propose A5 into a two-day window.

---

## 7. What I would do, absent answers

Stated as a recommendation rather than a decision, and explicitly conditional on Q1:

1. ~~Verify Q1 first.~~ **Done — Bleve is live in prod.** The remaining sub-question
   (index completeness) is worth ten minutes with a fresh API token, but it no longer
   blocks the plan below.
2. **Fix §2.1 and §2.3** (client filters + debounce). Hours, not days. Fixes the loudest
   symptoms and removes the noise that would otherwise distort any benchmark.
3. **Then §2.2** — push filters into the Bleve query (Option A1). This is the real
   engineering, and it fixes a class of wrong-results bugs rather than a symptom.
4. **Re-evaluate only then.** With correct filter combination and a debounced client, the
   remaining complaints will be about *relevance* — and that is a much sharper question,
   answerable by A1 tuning or A5, and no longer confounded by three unrelated defects.
5. **Treat GraphQL as a separate conversation** about the Library page's fan-out and field
   over-fetching. It has real merits there. It has none here.

**The thing I would most want to avoid:** choosing an engine to fix problems that are not in
the engine, and then concluding the new engine was a disappointment.
