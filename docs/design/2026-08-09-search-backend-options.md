<!-- file: docs/design/2026-08-09-search-backend-options.md -->
<!-- version: 3.2.0 -->
<!-- guid: 4f1c8a72-6d93-4e05-b8a1-9c72e0f45d38 -->
<!-- last-edited: 2026-08-10 -->

# Search, querying and sorting — the design we are going to build

**Status:** rewritten 2026-08-10 with the owner's answers. This is now a **direction with
open sub-questions**, not a menu of options, and it is the intended basis for a spec.
**Supersedes the option-survey version of this document entirely.**

---

## 0. What changed, and one correction

The first version framed the GraphQL question as *"conflating two axes — engine and API
shape."* **That was a mischaracterisation and it is withdrawn.** The question actually
asked was:

> would the querying be easier so we didn't have to have so many specific endpoints and
> constantly be adding new ones

That is a question about **API surface growth** — a real problem, separate from search
relevance, and answered on its own terms in §3 with numbers.

### Decisions taken

| # | Decision |
|---|---|
| 1 | **Engine: A1 + A5** — keep Bleve and push filters/sort into the query, **and** add hybrid lexical + vector search. |
| 2 | **API: explore B2 and B4 further** — structured query body, and typed RPC. §4. |
| 3 | **Sorting moves to the backend, in Go.** Client-side sorting of paginated library slices is replaced. |
| 4 | **"Not suck" is the bar** — the defects in §5 are not acceptable as permanent behaviour. |

---

## 1. Measured facts

Measured, not inferred. Prod memdb telemetry, 2026-08-09 10:34.

### 1.1 Scale — the three conflicting figures, resolved

The code carried three different numbers. They measure different things, and two were stale:

| memdb table | rows | est. resident bytes |
|---|---|---|
| **books** | **366,916** | ~878 MB |
| **book_files** (tracks) | **330,180** | ~350 MB |
| book_authors (junction) | 193,806 | ~16 MB |
| series | 28,420 | ~1.9 MB |
| authors | 18,294 | ~0.7 MB |
| book_narrators | 3,368 | — |
| narrators | 922 | — |
| author_aliases | 30 | — |

- **"392K-book"** (`memdb_strip.go:14`) was **books**, now 366,916. Not tracks — tracks are
  330,180 and are the *second* largest table.
- **"~68K unfiltered"** (`pebble_store.go:689`) and **"~38K primary"**
  (`memdb_summaries.go:18`) are **older figures for filtered subsets** at the time those
  comments were written. Neither describes the table.
- The user-facing count (~44,888 in the ABS handler comment, "forty-four thousand" in the
  Aug 8 summary) is the **primary-version** count — what a person means by "my library".
  366,916 is every row including non-primary versions.

**Consequence:** an index over *books* is a 366K-entry structure, not a 44K one. That is the
number to size against. Memdb warmup takes **107.9 seconds**.

### 1.2 The engine that already exists

`internal/search/` is a complete Bleve (scorch) integration: a hand-written query DSL with
lexer, AST and translator, facet counts, highlighting, per-user filters, an index builder,
and property tests. Registered as `searchindex`, index at `{datadir}/library.bleve`, routed
into the list path at `server_lifecycle.go:298`.

**Verified live in production:** `msg="Search index opened"` on the current process and on
every restart back to Aug 07.

⚠️ **`Open()` failures are downgraded to warnings** (`internal/search/register.go`), so the
server boots *without search* and silently falls back to an O(N) substring scan. Nothing
alerts on it. Whatever else gets built, **that fallback must become loud.**

### 1.3 API surface — the size of the problem

| measure | count |
|---|---|
| unique method+path routes across `internal/server` | **475** — 219 GET, 256 write |
| unique method+path routes under `/audiobooks` | **82** — 33 GET, 49 write |
| server-side sort fields (`sortFieldMap`) | 5 — `title`, `author`, `narrator`, `series`, `genre` |

> **Earlier figures in this document were wrong and are corrected here.** "680 route
> registrations" counted the same route registered in more than one file; "129 distinct
> paths under `/audiobooks`" came from grepping quoted strings, which swept up
> query-string variants (`/audiobooks?has_file_errors=true`) and non-routes
> (`/audiobooks.db`). The numbers above are unique `METHOD path` pairs from route
> registrations only, excluding tests.

---

## 2. Sorting: where it is, what it costs

### 2.1 Three implementations, one correct

| where | what it does | verdict |
|---|---|---|
| `service_filtering.go:130` `applySorting` | Go, server-side, over the full filtered set | **correct — keep and extend** |
| `ConfigurableTable.tsx:201` | `[...rows].sort()` on the client | **replace** — sorts the *current page* |
| `api.ts` `searchBooksPage` | sends no `sort_by` | **fix** — search silently drops the sort |

The client-side one is not merely misplaced. On a paginated library it reorders the 50 rows
already fetched, so "sort by title descending" returns **the wrong 50 books, correctly
ordered among themselves.** It looks like it works, which is why it survived.

**Scope:** 15 `.sort()` sites exist in `web/src` and most are fine — a book's own file list
by track number, tag clouds by count, metadata candidates by score. **The rule: a sort over
a paginated slice of the library is wrong; a sort over a complete set the client already
holds is fine.**

### 2.2 The real cost — and why it is not "load everything into RAM"

**The backend already holds the library in RAM**: 366,916 books and 330,180 files in an
immutable radix tree, ~1.25 GB by the telemetry above. `memdb_strip.go` records that
stripping `Description`, fingerprint blobs and `VersionNotes` cut it from **~10GB to ~2GB**.
That wall was hit and paid for already.

So the question is not *should Go hold it all* — it does — but **why sorting is still
expensive given that.** One branch (`service_query.go:155`):

```go
pdLimit, pdOffset := limit, offset
if heavySorting {
    pdLimit, pdOffset = 0, 0   // fetch the FULL filtered set
}
```

- **Title sort** streams off memdb's sorted radix tree, stopping at `limit+offset`. Cheap.
- **Every other sort** materialises the entire filtered set, sorts it, discards all but 50.

Note what that code is doing: it **disables pagination to keep sorting correct**, because a
sort applied after pagination only orders rows you already have. It chose slow over wrong.
The fix is to remove the choice.

There is precedent for the danger. `memdb_reads.go:585` records that sorting 68K books by
lowercase title per page load caused **340MB allocations per call and severe GC pressure** —
"never again". At 366K rows it is worse.

### 2.3 The fix: secondary sorted indexes

A secondary index stores **keys and IDs, not books**. Sized against the measured 366,916
books — short key + ID + tree overhead — expect **tens of MB per sort field** against
~1.25 GB resident: low single-digit percent each. Five fields is a bounded, predictable cost
that **deletes an unbounded per-request allocation.**

Each indexed field converts a full-set materialise-and-sort into the streaming walk that
title already gets.

---

## 3. Would a query language mean fewer endpoints?

Answered directly. **Partly — and not where you would expect.**

### 3.1 Sorts specifically are already parameterised

Adding a sort today is **one entry in `sortFieldMap`**, not a new endpoint. There are 5
fields; a sixth is a one-line map addition plus an index (§2.3). **No query language is
needed to stop adding endpoints for sorts, because we are not adding endpoints for sorts.**

### 3.2 But the proliferation is real, and it lives elsewhere

**82 routes under `/audiobooks`** (475 server-wide). They are overwhelmingly
**per-action and per-projection**:

```
/audiobooks/:id/alternative-titles     /audiobooks/:id/apply-metadata
/audiobooks/:id/changelog              /audiobooks/:id/changes
/audiobooks/:id/clear-no-match         /audiobooks/:id/compare-acoustid
/audiobooks/:id/cover-history          /audiobooks/:id/cover-history/restore
/audiobooks?has_file_errors=true       /audiobooks?missing_covers=true
```

Two pressures, wanting different answers:

**(a) Per-projection reads** — "the changelog", "alternative titles", "books with file
errors". **A query language genuinely collapses these.** Instead of a route per shape, the
client states the fields and filters it wants. This is where field-selection actually pays,
and it is a real reduction in surface area rather than a theoretical one.

**(b) Per-action writes** — `apply-metadata`, `clear-no-match`, `cover-history/restore`.
**A query language does not help.** GraphQL mutations are still one per action; you rename
`POST /x/:id/apply-metadata` to `mutation applyMetadata`. The count does not drop, it moves.

### 3.2.1 The read/write split, measured

This was the open question that decides the whole thing. It is now counted.

| scope | total | GET (reads) | writes | read share |
|---|---|---|---|---|
| whole server | 475 | **219** | 256 | **46%** |
| under `/audiobooks` | 82 | **33** | 49 | **40%** |

**Writes outnumber reads.** A query language collapses reads only, so the ceiling on what
it can retire is **33 of 82** routes under `/audiobooks`, and 219 of 475 server-wide. That
is an upper bound, not an estimate — it assumes *every* GET is collapsible.

**But those 33 GETs are unusually collapsible.** Enumerated:

```
/audiobooks/:id/alternative-titles   /audiobooks/:id/changelog
/audiobooks/:id/changes              /audiobooks/:id/cover-history
/audiobooks/:id/cow-versions         /audiobooks/:id/external-ids
/audiobooks/:id/field-states         /audiobooks/:id/files
/audiobooks/:id/metadata-history     /audiobooks/:id/metadata-history/:field
/audiobooks/:id/metadata-rejections  /audiobooks/:id/narrators
/audiobooks/:id/path-history         /audiobooks/:id/segments
/audiobooks/:id/tags                 /audiobooks/:id/tags-detailed
/audiobooks/:id/user-tags            /audiobooks/:id/versions
```

Eighteen of them are **"a different projection of one book"** — exactly the shape a query
language exists to collapse. Another handful are list variants (`/quarantined`,
`/soft-deleted`, `/count`, `/facets`, `/duplicates`) that are really `GET /audiobooks` with
a filter, and would fold into the same query endpoint.

The remainder are genuinely distinct resources — `/cover` and `/sample` stream binary and
should stay their own routes regardless.

**Conclusion:** the honest ceiling is ~40% of the surface, but the *concentration* is what
matters — roughly 20 of the 33 reads are one-book projections that a single well-shaped
query endpoint replaces. That is a real reduction, and it is the strongest concrete argument
for B2 in this document.

### 3.3 The cost side, stated honestly

- **Every e2e mock routes on REST paths.** That suite was just repaired at real cost and is
  now a blocking gate. A query-language migration rewrites all of it.
- **The response envelope** (`body.data`, with `getBookTags` / `getBookExternalIDs` as
  documented exceptions) changes shape.
- **Query-cost limiting becomes mandatory** — an unbounded client query over 366K rows is a
  self-inflicted outage.
- N+1 resolvers are easy to write, and this codebase has fought N+1/O(N²) repeatedly.

---

## 4. API shape: B2 and B4, explored

Both are live per decision 2. They are **not mutually exclusive** — B4 can carry B2's payload.

### 4.1 B2 — structured query body (`POST /audiobooks/query`)

One read endpoint taking a JSON filter/sort/projection object.

| pros | cons |
|---|---|
| Collapses the per-projection read paths (§3.2a) with no new runtime or schema language | POST-for-read complicates HTTP caching and bookmarkable URLs |
| Nested boolean filters as JSON rather than URL-encoded soup — the current `filters` param is already awkward | The URL is today the source of truth for Library state, and that plumbing works |
| Natural home for filter+sort pushdown (§2.3) — one place to validate and translate | Two list endpoints during migration, or a hard cutover |
| Server-side validation with real Go types; no resolver layer | Field selection is possible but hand-rolled |
| **Incremental and reversible** — ships beside `GET /audiobooks`, callers migrate one at a time | |

### 4.2 B4 — typed RPC (Connect / gRPC-Web)

| pros | cons |
|---|---|
| End-to-end types from one schema, generated for Go and TS — no drift between `api.ts` and the handlers | Another codegen step in a build already juggling Go + Vite + `//go:embed` |
| Connect speaks HTTP/1.1 + JSON, so `curl` works and e2e mocks stay interceptable | Doesn't reduce field over-fetching the way field-selection does |
| Streaming is first-class — relevant to scans, transcode and dedup, which today use SSE/polling | Same mock-rewrite cost as GraphQL |
| Strong fit: a Go server with essentially one first-party client | Per-action methods still proliferate (§3.2b) |

### 4.3 How they combine

**B2 answers "how do I ask for data without a route per question."**
**B4 answers "how do I stop client and server disagreeing about types."**

A Connect service whose `Query` method takes the B2 filter object gets both, without
adopting GraphQL's runtime.

**Sequencing:** B2 first, as a plain REST endpoint — incremental, reversible, and
immediately useful as the home for filter+sort pushdown. Evaluate B4 once there is one
well-shaped query surface worth generating types *for*, rather than generating types for 475
hand-written routes.

---

## 5. The three defects "not suck" requires fixing

1. **The client drops every filter and the sort when you type.** `api.ts` `searchBooksPage`
   sends only `search`/`limit`/`offset`/`is_primary_version`. The backend already applies
   `library_state`, tags, field filters and sort on the search path
   (`service_query.go:226`) — it would honour them today if the client sent them. **~1 hour.**
2. **Post-filtering runs after pagination.** `searchWithBleve` calls
   `SearchNative(bleveQ, offset, limit)`, then filters strip rows from the already-paginated
   page → short pages, wrong totals, skipped rows. Worked around for exactly one filter class
   via a 10,000-row over-fetch window. **This is the architectural one — the same defect as
   sorting-after-pagination.**
3. **No debounce.** Ten requests for a ten-character query, each a full corpus scan on the
   fallback path. **~30 minutes**, and it distorts any benchmark taken before it is fixed.

---

## 6. The plan

1. ~~**Client sends filters and `sort_by`; debounce the box.**~~ ✅ **DONE 2026-08-10**
   (#2264, #2265). Both turned out to be an existing mechanism being **bypassed**, not a
   missing one:
   - The 300ms debounce existed but `useLibraryQuery.ts:165` ignored it the moment the
     search parsed (`parsedSearch ? parsedSearch.freeText : debouncedSearch`), and
     `parsedSearch` was in the loader's dep array. Fixed by moving both off one timer.
   - The filters were dropped by a **branch** — `searchBooksPage` sends four parameters,
     `getBooks` sends all of them. Fixed by collapsing to one query path rather than
     widening the narrow one, so a future filter cannot be wired into one branch and
     forgotten in the other.

   Both `test.fixme` markers in `search-and-filter.spec.ts` are now passing tests
   (33 passed / 0 failed / 0 skipped).
2. **A1 — push filters AND sort into the Bleve query**, so both apply *before* pagination.
   The one piece of real engineering; removes the full-set materialise and fixes §5.2.
   **⚠️ Blocked on index reconciliation** — see open item 3. Pushing filters into an index
   that silently drops 56K updates a week converts a relevance problem into missing rows.
3. **Secondary sorted indexes** (§2.3) for the fields that matter, sized against 366,916.
4. **Delete the client-side library sort** and restore the missing sort control.
   `SearchBar.test.tsx:43` currently asserts the control is *absent* and passes vacuously —
   that assertion will defend the bug unless inverted.
5. **B2** — `POST /audiobooks/query` beside the existing endpoint; migrate callers
   incrementally; make it the home for step 2's pushdown.
6. **A5 — hybrid lexical + vector.** The substrate exists: `coder/hnsw` in `go.mod`, local
   bge-m3 embeddings via Ollama, an HNSW snapshot load path — all built for dedup. This is
   the only item that **adds a capability** rather than fixing a defect, and it belongs after
   the foundation is correct.
7. **Evaluate B4** once step 5 has produced a query surface worth generating types for.

**Not chosen:** hand-rolled Pebble inverted indexes (months; re-implements a search engine);
SQLite FTS5 (reopens a settled decision — `docs/database-architecture.md` has a "Why Not
SQLite3?" section and there is no sqlite dep in `go.mod`); an external search service
(breaks single-binary deployment for a self-hosted install).

---

## 7. Still open

1. ~~What is the read/write split?~~ **ANSWERED — see §3.2.1.** 46% reads server-wide,
   40% under `/audiobooks`; ~20 of the 33 reads are one-book projections that a single
   query endpoint would replace.
2. **Which sort fields matter?** Each costs an index. Five exist; "all of them" is the
   expensive answer, and probably nobody sorts by publisher.
3. **🔴 Index staleness — now a BLOCKING prerequisite, not a question.** Filter/sort
   pushdown promotes the Bleve index from a *relevance* dependency to a **correctness**
   one. Item 4 below measured the index dropping 56,537 updates in a week with no
   reconciliation. Today that means stale ranking; after A1 it means **a book whose
   `library_state` changed is absent from the correct filter and present in the wrong
   one**, with no error shown. **Reconciliation has to land before step 2 of §6.**
4. ~~Is the index complete?~~ **ANSWERED — NO.** Measured on prod 2026-08-10: the index
   worker's 1024-deep queue overflows under bulk operations and **silently drops** the
   overflow — **56,537 dropped operations in seven days** (Aug 03 and Aug 07). There is no
   retry, no dirty-set and no re-sync, so a dropped update diverges the index from the DB
   permanently. See `todo.d/20260810-search-index-queue-drops-silently.md`.
5. **Per-user filters** — there is a `DisablePerUserSearchFilters` flag and a 10,000-row
   over-fetch window. If multi-user is theoretical, that path simplifies and §5.2 gets easier.
