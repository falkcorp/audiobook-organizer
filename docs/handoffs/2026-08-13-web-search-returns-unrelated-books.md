<!-- file: docs/handoffs/2026-08-13-web-search-returns-unrelated-books.md -->
<!-- version: 3.0.0 -->
<!-- guid: b2f4a7c1-53de-4e08-9a16-7cc0e5d1f3b8 -->
<!-- last-edited: 2026-08-13 -->

# Handoff: web-UI search returns unrelated books

**Status: RESOLVED 2026-08-13 (v3.0.0).** Two sessions investigated this in parallel and
reached opposite conclusions. Reconciled here against production data: **hypothesis A
(the index does not contain the book) is CONFIRMED; hypothesis B (nil index / substring
fallback) is REFUTED; and the v2.0.0 conclusion that "search is fine, a primary-less
`vg-` group is hiding it" is WRONG for the reported book** — its version group has two
members and *does* elect a primary. See "Correction to v2.0.0" below before reading the
v2.0.0 section, which is kept intact because its `vg-` observation is a real, separate
lead that just needs re-counting.
**Reported:** 2026-08-13 by the owner.

---

## Correction to v2.0.0 (added v3.0.0)

The v2.0.0 section below concludes that the reported book is hidden because its copies
sit in `vg-`-prefixed **singleton groups that elect no primary**. That is not what the
data says. A **third** row carries this title, and v2.0.0 never saw it:

```
id=01KZRBPPCFP6PV39F4Q5CZZZ85  primary=TRUE   group=vg-01KXXVFEHRXGXMKPDYMY6HENDK
id=01KXXVFEKD3XE0VBQ5HVXNWB86  primary=False  group=vg-01KXXVFEHRXGXMKPDYMY6HENDK   <- v2.0.0 called this a primary-less singleton
```

**Same group.** It has (at least) two members and it *does* elect a primary. So the
`is_primary_version=true` filter is not what removes this book from the results — there
is a perfectly good primary row for the filter to keep.

It is missing for a different reason: **`01KZRBPPCF…` is absent from the Bleve index.**
It returns from a direct store lookup and returns nothing under three separate queries
whose terms are all in its title. The two rows that *do* rank #1–#2 without the filter
are the two non-primary copies, which *are* indexed. Filter to primaries and the only
row that qualifies is the one the index cannot see, so the result is the five
description-matches — the owner's screenshot.

**Why v2.0.0 could not see it:** it enumerated rows by *searching*. A row missing from
the search index is invisible to that method by construction, so the enumeration found
two rows, saw both non-primary, and concluded no primary existed.

**The 6,157 figure needs re-deriving before anyone acts on it.** It was computed by
paging the `is_primary_version=false` set and grouping by `version_group_id`. A group's
primary member is *by definition excluded from that set*, so "one book per group" in
that data can only mean "one **non-primary** book per group" — which is also the shape
of a perfectly healthy two-member group. `vg-01KXXVFEHRXGXMKPDYMY6HENDK` is a concrete
counterexample: it is inside the 6,157 and it has a primary. Re-count by fetching each
candidate group's *full* membership before calling it primary-less.

To be explicit about what survives: **`vg-` prefixed group ids appearing only in a
bounded creation window is still an interesting, unexplained observation** and worth
chasing. What does not survive is "these groups have no primary" and the count attached
to it.

---

## The actual cause (v2.0.0 — SUPERSEDED, see correction above)

The search engine returns the right answer. For `All Jobs and Classes` the API returns
`count=8` with the owner's book ranked **first and second**. The web UI then discards
both, because the Library page filters to primary versions by default and **neither copy
is flagged as one.**

Adding the UI's own filter to the API call reproduces the owner's screenshot exactly —
the same five books, no target:

```
?search=All%20Jobs%20and%20Classes                       -> count=8, target ranked #1 and #2
?search=All%20Jobs%20and%20Classes&is_primary_version=true -> count=5, target GONE
```

The five survivors are legitimate low-boost matches on `description` and other non-title
fields. They are not noise from a broken query; they are what is left after the real
match is filtered out.

### Why the books are invisible

```
id=01KXXVFEKD3XE0VBQ5HVXNWB86  primary=False  group=vg-01KXXVFEHRXGXMKPDYMY6HENDK
id=01KXXVBGQGH6PEP9WE0ZWHBJ50  primary=False  group=vg-01KXXVBGMHPATT8X1X3DV5AW2Q
id=01KNDCC3F1BASPVH21TVV8J78R  primary=True   group=01KNDCC3F1BASPVH21TX0N8KZH   (Dragon Conjurer)
```

Healthy books sit in unprefixed version groups where exactly one member is primary
(verified on a two-member group: *Dungeon Diving Culinary Wizard* 1 and 2 share one group
and elect one primary). The broken books sit in **`vg-`-prefixed groups, one book per
group, with no primary elected at all**. A singleton group whose only member is not
primary can never satisfy `is_primary_version=true`.

### Blast radius: 6,157 books, 9.6% of the library

Counted exhaustively by paging the full non-primary set (23,031 books, 24 pages of 1,000)
and testing the `version_group_id` prefix:

| | |
|---|---|
| total books | 63,870 |
| `is_primary_version=true` | 40,839 |
| `is_primary_version=false` | 23,031 |
| **of those, `vg-` singletons with no primary** | **6,157** |
| distinct `vg-` groups | 6,157 (one book each — all singletons) |

The affected books are contiguous in creation order: every one falls between
**2026-04-04 and 2026-08-11**, and the scan found zero `vg-` rows in the older 11,031.
That bounds this to one code path introduced or active in that window — find what mints a
`vg-`-prefixed group id and you have the writer.

**This is not a search bug. These 6,157 books are invisible everywhere the web UI applies
its default primary-version filter** — library browsing, counts, and any filtered view —
not only in search. The ABS mobile app does not apply that filter, which is the whole
explanation for the web-vs-app split that this document was originally written to chase.

### What to do next

1. Find the writer that mints `vg-`-prefixed `version_group_id` values and does not elect
   a primary. That is the defect; everything else here is a symptom.
2. Decide the repair for existing data: a singleton group's only member should almost
   certainly be its primary. A backfill that elects the sole member of any
   single-member group is the obvious candidate — **but confirm the group really is a
   singleton before flipping the flag**, or a real duplicate pair gets two primaries.
3. Add an invariant test: no version group may have zero primaries. This class of defect
   is exactly what an invariant suite catches and no unit test will.
4. Only then revisit search. The two genuine search defects recorded further down
   (stopword dropping, quoted phrases) are real and still worth fixing, but neither is
   the reported bug.

---

## Original v1.0.0 investigation — kept as a record of what was ruled out

Everything below was written before the cause was known. The "Eliminated" section is
still accurate and still useful.

> **v3.0.0 correction:** v2.0.0 marked *both* still-open hypotheses disproven on the
> grounds that "Bleve found the books and ranked them first." Bleve found the two
> **non-primary** copies. It did not find the primary one. **Hypothesis A is therefore
> CONFIRMED, not disproven** — see "Correction to v2.0.0" at the top. Hypothesis B *is*
> correctly refuted, though by a different argument: `PebbleStore.SearchBooks` is a
> whole-query substring matcher, so if it were serving, `search=jobs classes` would
> return 0 rather than 8, and reversing the word order would not return a byte-identical
> set.

---

## Resolution (2026-08-13)

### The bug reproduced exactly

With the parameters the Library page actually sends (`is_primary_version=true`, which
the original report did not mention), production returned **exactly the owner's five
books and nothing else**:

```
search=All Jobs and Classes&is_primary_version=true  ->  count=5
  Dragon Conjurer / Parallax Rising / All in Charisma /
  Dungeon Diving Culinary Wizard / Solo Leveling, Vol. 2
```

Without that filter the same query returns **8**, with the two correct books ranked
**1 and 2**. So relevance was never the problem, and the "unfiltered slice of the
library" reading was wrong: all five survivors legitimately match, on the
**description** field (boost 0.5 — visible in the query JSON), because after stopword
dropping the query is `job AND class` stemmed, and each decoy's description contains
both ("*I went to class, worked my minimum-wage job*").

### Why the correct books vanished — Hypothesis A, narrowed

Three rows carry the title (which is what the app returns, closing the 3-vs-2 gap):

| id | primary | in Bleve |
|---|---|---|
| `01KXXVFEKD…` | false | yes |
| `01KZRBPPCF…` | **true** | **NO** |
| `01KXXVBGQG…` | false | yes |

The index does contain the title — but **not the primary row**, which is the only row
the Library page's filter keeps. `01KZRBPPCF…` (created 2026-08-11) returns from a
direct memdb lookup and is absent from Bleve under three different queries whose terms
are all in its title. Four further distinctive-title probes behaved identically, with
result counts of 4–9, so a top-N ranking confound is excluded.

The gap is systemic and shaped like a step function:

| created | searchable | missing | coverage |
|---|---|---|---|
| 2026-04 | 38 | 1 | **97%** |
| 2026-08 | 1 | 50 | **2%** |

(Sampling caveat: a first pass reported 2026-04 at 73%. That was a measurement
artifact — querying full titles containing `:` makes the server DSL parse `System:` as
`field:value`. Stripping punctuation removed it. Result sets at the page cap are
counted "inconclusive" rather than "missing", since a capped set cannot prove absence.)

### Mechanism

`buildSearchIndexIfEmpty` (`internal/server/server_search.go`) is the only bulk build
and gates on `DocCount() == 0` — "non-empty means complete". The loop honours
`s.bgCtx`, so a shutdown part-way through leaves a populated-but-incomplete index; the
next boot sees `count > 0` and returns early, making the gap **permanent**. It walks
books in ULID order and ULIDs are time-ordered, so a cancellation always loses the
**newest** books — which is exactly the step function measured.

The #2268 dirty-set reconciler does not cover it. `markIndexDirty` is called from
exactly one place — `enqueueIndex`'s queue-full branch — so it repairs *dropped* events
only. A book the backfill never reached was never enqueued, is never dirty, and is
never reconciled. (The reconciler itself *is* started, at
`server_lifecycle.go:301` — that was verified, not assumed.)

### Why Hypothesis B is refuted

`PebbleStore.SearchBooks` is a **whole-query substring** match over
title/author/narrator only; it never tokenises and never reads description. If it were
serving, `search=jobs classes` would return 0. It returned 8, and `classes jobs`
returned a byte-identical set — order-independent token AND, i.e. Bleve. There is also
no version-group collapse anywhere on the query path (checked, because a dedup keeping
the wrong group member would have produced the same five-row output with a healthy
index); the only primary handling is a per-book pass/fail filter.

### Fix

`internal/server/search_coverage.go` — on boot, compare indexed docs against book
count and, when short, mark the books dirty so the shipped reconciler re-indexes them.
It seeds the durable dirty set rather than re-running the build, because re-running
would reuse the same non-resumable, cancellable loop that created the permanent gap.
Plus the logging this document asked for on all three silent fallbacks.

Regression tests are in `internal/server/search_coverage_test.go` — deliberately in
`internal/server`, not `internal/search`: a search-layer test seeds its own index and
therefore *cannot* fail on missing documents. Both were proven to fail with the fix
disabled (`3 docs indexed, want 6`) and restored byte-identical.

### Not fixed here, filed separately

The two independent defects this document records (stopword dropping, quoted phrases
not becoming phrases) are both confirmed **measured** in the query JSON — neither
causes this bug. They are in `todo.d/20260813-search-index-coverage-followups.md`
along with the prod-rebuild decision, an unexposed metric, and a version group with no
primary member.

---

## The report

Searching the web Library page for `All Jobs and Classes` returns five books that have
nothing to do with the query: *All in Charisma*, *Dragon Conjurer*, *Dungeon Diving
Culinary Wizard*, *Parallax Rising*, *Solo Leveling Vol. 2*. The owner also tried
`all jobs`, `all jobs and`, and the quoted `"All Jobs and Classes"` — all bad.

**The same search in the AudiobookShelf mobile app returns the correct 3 books.** So the
data exists, and at least one index knows about it. This split is the single most
valuable clue in the report and any hypothesis must explain it.

The result set looks like *an unfiltered slice of the library*, not a bad-relevance
ranking. That is the signature of a search term that never reached the query — not of a
query that matched poorly.

---

## Eliminated — do not re-investigate

### 1. The Bleve translator is correct

A precision test was written against the real index (`ParseQuery` → `Translate` →
`SearchNative`) with the owner's exact title seeded alongside the exact four decoy books
from the screenshot. **All five query forms returned `total=1`, correct top hit.**

The test file was deliberately left in the tree at
`internal/search/zz_repro_alljobs_test.go` (throwaway naming; delete it or promote it).
Run it:

```
go test ./internal/search/ -run TestReproAllJobsAndClasses -count=1 -v
```

The emitted query JSON is in the test log and is worth reading — it shows exactly what
the server builds.

### 2. The client-side parser does not blank the term

`web/src/utils/searchParser.ts` `parseSearch()` was traced by hand for all four inputs.
None of them contain a `:`, so `tryMatchFieldValue` returns null for every token and
every word falls through to `freeTextParts`. `freeText` comes back as the **full original
string** in all four cases. The parameter is sent.

(This was the leading hypothesis and it is wrong. It is still worth knowing because of
the live footgun in the next section.)

---

## Still open — start here

> **Superseded 2026-08-13.** Both hypotheses below have been decided against a running
> server: **A confirmed** (narrowed — it is the *primary* row that is missing, not the
> title), **B refuted**. Kept verbatim because the reasoning is what made the decisive
> tests cheap. See "Resolution" above.

### Hypothesis A (strongest): the Bleve index does not contain these books

This is the only candidate that cleanly explains why the ABS app finds them and the web
does not — **they are different search paths**. The web Library page goes through Bleve;
confirm what the ABS surface actually uses before assuming it shares the index.

Supporting prior: the search index has a documented history of silently dropping updates
(56,537 drops in 7 days, dirty-set + reconciler shipped in #2268). A book indexed before
that fix, and never re-indexed, would be invisible to Bleve and visible everywhere else.

Also observed in the shutdown log at 16:42 on 2026-08-13:

```
level=WARN msg="chromem hydrate finished with error" err="context canceled"
                                                    books=20656 authors=5521
```

That is the **chromem** vector store, not Bleve — do not conflate them — but it shows
hydration being cancelled at 20,656 of 67,824 books, and it is worth checking whether the
Bleve hydrate has the same cancel-on-shutdown behaviour and the same partial coverage.

**How to decide it, cheaply:** query prod for a book you know exists by a single
distinctive term (`jobs`) and compare the count against a direct DB/memdb lookup. If
Bleve returns nothing and memdb returns the three books, this is confirmed and the fix is
an index rebuild plus a coverage check, not a code change.

### Hypothesis B: the search index is nil in prod and the fallback is the thing misbehaving

`internal/audiobooks/service_query.go:105` branches on `svc.searchIndex != nil`. When
nil, it falls through to `svc.store.SearchBooks(search, limit, offset)` — a completely
different matcher whose multi-word behaviour has not been characterised here.

`searchWithBleve` **also** silently falls back to the same `store.SearchBooks` on a
parser error (line 666) or a translate error (line 677), with no log line on either path.
If the fallback is what is running, nothing in the logs would say so. That is worth
fixing regardless of this bug: **add a log line to both fallback branches.**

**How to decide it:** check whether the index initialised at boot, and instrument or
temporarily log which branch serves a request.

---

## Two real defects found on the way — independent of the main bug

Both are genuine, both are currently invisible, neither is proven to be *this* bug.

### `all` and `and` are English stopwords and are silently dropped

**Confirmed live 2026-08-13:** `?search=all%20jobs` returns `count=283`, topped by *The
Icarus Job* and *Side Jobs* — because the server actually searched `jobs` alone. This is a
real, separate, user-visible defect and it is why the owner's `all jobs` attempt looked
just as broken as the others, for an entirely different reason than the main bug.

`dropStopwordOnlyConjuncts` (`internal/search/bleve_translator.go:150`) strips conjuncts
that analyse to zero tokens. This exists for a good reason — it fixed "shards of
oblivion" returning nothing on 2026-08-11 — but it means:

| user types | server actually searches |
|---|---|
| `all jobs` | `jobs` |
| `all jobs and` | `jobs` |
| `All Jobs and Classes` | `Jobs AND Classes` |

Verified in the query JSON emitted by the repro test. For a library where *All* is a
common title word this materially widens results, and the user is given no indication
that half their query was discarded.

### Quoted phrases are not phrases

`"All Jobs and Classes"` does **not** become a `MatchPhraseQuery`. The server-side parser
never strips the quote characters, so the terms become `"All` and `Classes"` — the
closing quote stays glued to the final token. The translator's `n.Quoted` branch
(`bleve_translator.go:317`) exists and works; it simply never fires for this input.

It currently *appears* to work because the English analyzer strips the quote as
punctuation, so `Classes"` still matches `classes`. That accident is also why
`TestMultiWordFreeTextFindsTheBook`'s `{"quoted phrase", "\"Ascend Online\""}` case
passes. Phrase search is not doing what the UI's help text implies.

---

## Why the existing test suite cannot see any of this

`internal/search/multiword_repro_test.go` asserts only that the wanted book is
**somewhere in the hits**:

```go
if total == 0 { t.Fatalf(...) }
// then: is tc.want anywhere in hits?
```

It never asserts that unrelated books are absent, and never checks the top hit. **A
`MatchAll` returning the entire library would pass every one of its ten cases.** It was
written for a "finds nothing" bug and it is correctly shaped for that bug — but it is
structurally blind to the "finds everything" bug reported today.

Whatever the fix turns out to be, the regression test must assert **precision** (unrelated
books absent, correct book first), not presence. The repro test left in the tree is
already written that way; reuse its shape.

---

## Architectural note for whoever picks this up

There are **two independent query parsers** that must agree and are not tested against
each other:

- `web/src/utils/searchParser.ts` — client-side, 255 lines, field:value + negation
- `internal/search/query_parser.go` + `bleve_translator.go` — server-side DSL

The client parses the box, extracts `fieldFilters`, and forwards only `freeText`. The
server then parses that free text **again** with different rules (stopwords, quoting,
whitespace-as-AND). Any divergence between them is invisible from either side alone.

The delivery line worth staring at is `web/src/hooks/useLibraryQuery.ts:258`:

```ts
search: searchText || undefined,
```

An empty `freeText` — from any future parser change — silently becomes "no search
parameter", and the server answers with the unfiltered library rather than an error. That
is the same accepted-but-ignored-parameter family as the three ABS bugs fixed earlier
today (see `todo.d/2026-08-13-abs-ignored-query-params-sweep.md`); the endpoint cannot
distinguish "no search" from "search that vanished". Consider making the empty case
explicit rather than falsy-coalesced.

---

## Environment note

Production was mid-deploy (`systemctl` reported `deactivating`, `:8484` not listening)
when this investigation ran, so **no live probes were possible** and none of the
production-side claims above have been checked against a running server. Every
production statement in this document is drawn from the shutdown log or from prior
records, and is labelled as hypothesis where it is one. Re-probe once the deploy settles.
