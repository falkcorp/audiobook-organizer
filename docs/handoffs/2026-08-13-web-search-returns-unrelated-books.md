<!-- file: docs/handoffs/2026-08-13-web-search-returns-unrelated-books.md -->
<!-- version: 1.0.0 -->
<!-- guid: b2f4a7c1-53de-4e08-9a16-7cc0e5d1f3b8 -->
<!-- last-edited: 2026-08-13 -->

# Handoff: web-UI search returns unrelated books

**Status:** root cause NOT yet established. Four candidate mechanisms; two eliminated by
test, two still open and both cheaply decidable against production.
**Reported:** 2026-08-13 by the owner.
**Do not start by re-reading the translator.** It has been tested and it is correct — see
"Eliminated" below. Starting there will burn an hour reproducing a passing result.

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
