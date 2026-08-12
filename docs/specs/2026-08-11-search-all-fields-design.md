<!-- file: docs/specs/2026-08-11-search-all-fields-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7c4e2a91-5d38-4b06-9e17-2f8a6b3c04d5 -->
<!-- last-edited: 2026-08-11 -->

# Search Across All Fields — Design

**Status:** DRAFT. Measurements below are real and reproducible; the design is a
proposal and nothing here is implemented.
**Trigger:** owner report 2026-08-11 — *"there needs to be a way to search all
fields"*, and separately *"your search sucks ass, but when I search from the
audiobook app, it shows me the complete list of all that's there."*

---

## 1. Start here: there are TWO problems, and field coverage is the second one

It would be easy to read the request as "add more fields to the index" and start
there. That would have been wasted work. The measurements say the index does not
contain the library.

### 1.1 The measurement

Same server, same books, four different paths, 2026-08-11 ~23:55 EDT, all
cache-busted:

| Path | Result |
|---|---|
| Web search `All the Skills` | **1** |
| Web search `All the Skills` + `is_primary_version=true` | **0** |
| Web **filter** `title = All the Skills` (no search index involved) | **16** |
| The owner's audiobook app (ABS browse endpoint) | **18** |

And on the owner's original query:

| Query | Result |
|---|---|
| `author:"honour"` | 1 |
| `honour` | 1 |
| `author:honour` | 1 |
| `Honour Rae` | 1 |

### 1.2 What that means

**The Bleve index is missing ~94% of the matching rows.** The filter path reads
the database and finds 16. The search path reads the index and finds 1. The app
finds 18 because the ABS browse endpoint does not use the search index at all.

Worse: **the single indexed book is a non-primary version.** The library UI sends
`is_primary_version=true` by default (`web/src/services/api.ts:981`), so the one
hit is then discarded and the user sees **zero results**. That is why search reads
as totally broken rather than merely incomplete — the failure is amplified by a
filter applied after the search.

### 1.3 Why this is plausible and not a surprise

There is prior art in this exact component: the search index was found to be
**silently dropping updates — 56,537 in seven days**. A durable dirty-set plus a
reconciler shipped (#2268) to stop the bleeding. What that fix did **not** do is
**backfill the rows already missing**. A reconciler that keeps the index in step
from now on leaves every previously-dropped row absent forever.

**Not verified:** that pre-#2268 drops are the cause of *these* particular
absences. The alternative — that these rows were never indexed because indexing
is gated on something (library state, primary-version, a scan phase they never
reached) — has not been ruled out. **Measure before building.** See §5.1.

### 1.4 Sequencing, and why it matters

> Adding fields to an index that is missing 94% of its rows makes the search
> wrong in more ways, not fewer.

Do §5 (coverage) before §4 (fields). A user who searches a newly-added field and
gets nothing cannot tell "that field isn't indexed" from "that book isn't
indexed", and we will have made the system harder to debug, not easier.

---

## 2. Current state — what is actually searchable today

### 2.1 The index mapping

`internal/search/bleve_index.go:275-355`. **24 fields**, in four classes:

**Analyzed text** (English analyzer, `IncludeInAll = true`) — 7:
`title`, `author`, `narrator`, `series`, `publisher`, `description`, `file_path`

**Keyword / exact** — 9:
`tags`, `format`, `genre`, `language`, `library_state`, `isbn10`, `isbn13`,
`asin`, `_type`

**Numeric** — 8:
`year`, `series_number`, `duration_seconds`, `bitrate_kbps`, `sample_rate_hz`,
`channels`, `bit_depth`, `file_size_bytes`

**Boolean** — 1: `has_cover`

**Date/time: none.** There is no datetime field mapping in the index at all.

The document that feeds it (`internal/search/document.go`) carries exactly these
fields and no others.

### 2.2 How free text is dispatched

`internal/search/bleve_translator.go:310-331`, `translateFreeText`:

```go
if n.Prefix { return bleve.NewPrefixQuery(n.Value) }
if n.Fuzzy  { return bleve.NewFuzzyQuery(n.Value) }
if n.Quoted { return bleve.NewMatchPhraseQuery(n.Value) }
children := ...                       // one MatchQuery per textFieldBoosts entry
children = append(children, bleve.NewMatchQuery(n.Value))   // unfielded → _all
return bleve.NewDisjunctionQuery(children...)
```

Two things follow, and both are latent bugs independent of everything else:

- **A plain term** fans out across the boosted text fields *plus* an unfielded
  child that reaches `_all`. Reasonable.
- **Prefix, fuzzy and quoted terms return early**, before `textFieldBoosts` is
  consulted. They emit a single unfielded query and depend entirely on `_all`.
  So `"exact phrase"`, `pref*` and `fuzzy~` **silently use a different field set
  and different weighting** from the bare term next to them in the same query.
  Nothing documents this and no test asserts it.

### 2.3 What `_all` does and does not contain

Bleve's `NewTextFieldMapping()` and `NewNumericFieldMapping()` both default
`IncludeInAll` to true, so every *mapped* field is reachable unfielded. The
explicit `f.IncludeInAll = true` in `textAnalyzed()` is redundant.

**This is the crux: `_all` is not "all fields on the book". It is "all fields we
chose to map."** A user typing a bare word is searching 24 fields out of ~105.
There is no mechanism today by which an unmapped field could ever match.

---

## 3. The gap, enumerated

`database.Book` (`internal/database/store.go`) carries **~105 JSON fields**. 24
are indexed. Below is what is missing, grouped by why it matters. Every name is
taken from the struct tags, not invented.

### 3.1 Free text a human would expect to search — currently impossible

| Field | Why it matters |
|---|---|
| `intro_transcription` | **The actual transcript text.** ~96.5% of the library is transcribed. This is the single highest-value unindexed field: it is the only ground truth for what a file actually contains, and it is exactly what you want when the metadata is wrong. See §4.4 — it is also the most expensive. |
| `transcribed_title`, `transcribed_author`, `transcribed_narrator`, `transcribed_translator`, `transcribed_cover_artist` | What the audio *says* it is, as opposed to what the tags claim. Invaluable when the tags are contaminated, which is a known condition of this library. |
| `original_filename` | The pre-organize name. Often the only surviving clue for a mis-titled book. |
| `narrators_json` | Only the single `narrator` string is indexed. Multi-narrator books are unfindable by their other narrators. |
| `edition`, `version_notes` | Distinguishing editions is the whole point of version groups. |
| `source_import_path`, `itunes_path` | Only book-level `file_path` is indexed. |
| `quarantine_reason`, `transcribe_error` | Operator triage — "show me everything that failed for reason X" is impossible. |
| `user_rating_notes` | The owner's own notes are not searchable. |
| `metadata_source`, `metadata_provenance` | "Which books did provider X touch?" |
| `codec`, `quality` | `format` is indexed, `codec` is not. |

### 3.2 Identifiers — only 3 of 12 are searchable

Indexed: `isbn10`, `isbn13`, `asin`.

Not indexed: `id`, `work_id`, `version_group_id`, `merged_into_book_id`,
`author_id`, `series_id`, `open_library_id`, `hardcover_id`, `google_books_id`,
`itunes_persistent_id`, `book_sig_v1`.

Pasting a book ID into the search box — the most natural thing to do when
debugging — returns nothing.

### 3.3 Status and enum fields — 1 of 9

Indexed: `library_state`.

Not indexed: `metadata_review_status`, `transcribe_status`, `fingerprint_status`,
`itunes_sync_status`, `is_primary_version`, `marked_for_deletion`, `needs_rescan`,
`quality`.

`is_primary_version` is the notable one: it is applied as a **post-search filter**
today, which is what turned a 1-result search into a 0-result search in §1.1.
Being able to express it *inside* the query removes that whole class of surprise.

### 3.4 Numerics — 8 of ~22

Not indexed: `coverage_percent`, `book_sig_coverage_pct`, `quantity`,
`total_file_count`, `fingerprinted_file_count`, `audible_runtime_min`,
`itunes_play_count`, `itunes_rating`, and every rating field —
`audible_rating_overall`, `audible_rating_performance`, `audible_rating_story`,
`audible_rating_count`, `audible_num_reviews`, `google_rating_average`,
`google_rating_count`, `user_rating_overall`, `user_rating_story`,
`user_rating_performance`.

Also: the index has one `year`, but the model has **two** — `print_year` and
`audiobook_release_year`. Which one populates `year` is not documented.

### 3.5 Dates — 0 of 14

`created_at`, `updated_at`, `metadata_updated_at`, `last_written_at`,
`last_organized_at`, `quarantined_at`, `last_fingerprinted_at`,
`itunes_date_added`, `itunes_last_played`, `intro_transcribed_at`,
`transcribe_attempted_at`, `book_sig_built_at`, `duration_verified_at`,
`marked_for_deletion_at`.

**There is no date field mapping in the index at all**, so no date query can be
expressed. "What did I import last week", "what has not been touched since
April", "what was organized during the run that broke things" — none are askable.

### 3.6 Not represented at all: `BookFile`

The index is book-level only. Per-file paths, per-file transcripts, and per-file
tags cannot be searched. For a library where 199 books are shattered across 6,060
single-file folders, per-file path search is not an edge case.

---

## 4. Design

### 4.1 Principle: default-in, explicit opt-out

Today's mapping is an allowlist, and the allowlist is the bug — every field added
to the model since the mapping was written is invisible, silently, forever, with
no failure anyone can see.

**Invert it.** Derive the search document from the `Book` struct and index every
field by default, with an explicit `search:"-"` opt-out for the ones that must not
be indexed. A new field on the model then becomes searchable *by default*, and
excluding it becomes a deliberate, reviewable act.

This is the same lesson as the `todo.d` header exemption and the discarded-error
sweep: **make the tool not care, then check the artifact, then write prose.** A
document saying "remember to add new fields to the search mapping" is the weakest
possible mechanism and we already know it does not hold.

Suggested opt-outs — decide explicitly, do not inherit this list:
`book_sig_v1`, `book_sig_segments`, `book_sig_v1_mask` (binary signatures, no
human ever searches them), and anything per-user (§4.5).

**Enforcement:** a test that reflects over `database.Book` and fails when a field
is neither mapped nor tagged `search:"-"`. That is the piece that makes this
durable; without it §4.1 decays back into an allowlist within two months.

### 4.2 Field classes and analyzers

| Model type | Mapping | Notes |
|---|---|---|
| Free-text prose | analyzed text, English analyzer | title, description, transcripts |
| Names | analyzed text | author, narrator, publisher, series |
| Paths | analyzed text with a path-aware tokenizer | splitting on `/`, `-`, `_` matters; the current English analyzer on `file_path` is doing something close to nothing useful |
| IDs / enums | keyword | exact match, no stemming — an ASIN must not be stemmed |
| Numbers | numeric | enables range queries |
| Booleans | boolean | |
| **Timestamps** | **datetime — new, none exist today** | enables `created_at:>2026-08-01` |

### 4.3 Query surface

Keep the existing parser (`internal/search/query_parser.go`) and extend it:

- `field:value` for every indexed field, with the field name matching the JSON
  tag so what a user sees in the API is what they can type.
- **Aliases** for the ones people will get wrong: `narrator` → also match
  `narrators_json`; `year` → both `print_year` and `audiobook_release_year`;
  `id` → any identifier field. Aliases are where "search all fields" stops being
  a slogan and starts being usable.
- Date ranges: `created_at:>2026-08-01`, `updated_at:[2026-07-01 TO 2026-08-01]`.
- Numeric ranges on all numeric fields, not the current 8.
- A bare term keeps fanning out across weighted text fields plus `_all`.

**Fix the prefix/fuzzy/quoted inconsistency from §2.2 as part of this.** All three
must fan out over the same field set as a bare term. This is a real behaviour bug
and it should carry its own test, with a negative control.

### 4.4 Transcripts — the one decision that needs a number first

`intro_transcription` is the highest-value unindexed field and the only one with a
serious cost question. Before indexing it:

1. **Measure.** Total transcript bytes across the library, and the resulting index
   growth. Do not estimate — this codebase has already produced one wrong
   conclusion by extrapolating a per-row measurement against an inflated row
   count (the sort-index estimate, off by ~7×).
2. If the cost is acceptable, index it as analyzed text but **exclude it from
   `_all`** (`IncludeInAll = false`) and give it a low query-time boost. A bare
   search for a common word should not be dominated by transcript prose; but
   `transcript:"the name of the wind"` should work.
3. If it is too large, index only the first N words. Say so in the docs and in the
   UI, because a silently truncated index is the same failure mode as a silently
   dropped row.

### 4.5 Per-user fields stay post-filter — for now

`perUserFieldSet` (`internal/search/bleve_translator.go`) already routes per-user
predicates to a post-filter. **Post-filtering after pagination is a known defect
in this codebase** and is why result counts have been wrong before.

Out of scope here, but do not extend the post-filter set while working on this.
The open design question — how to express per-user visibility *inside* the query
rather than after it — is tracked separately and is a prerequisite for per-user
search being correct at all.

---

## 5. Sequencing

### 5.1 Phase 0 — coverage (blocking, do this first)

1. Count documents in the Bleve index; compare to the book count in the database.
   Report both numbers. This single measurement decides everything below.
2. Determine **why** rows are missing. The two hypotheses:
   (a) pre-#2268 silent drops that the reconciler never backfilled;
   (b) indexing is gated on a condition these rows never met.
   These need different fixes. Do not guess.
3. Build a **full reindex** operation, resumable and bounded. Note that a naive
   implementation is exactly the shape that OOM-killed this server on 2026-08-11:
   full-scan, unmarshal everything, no budget, no cancellation. Use a bounded
   worker pool per the concurrency rules in `CLAUDE.md`, honour `ctx`, and stream.
4. Add a **coverage invariant check** — indexed count vs book count — surfaced as
   a metric and an operator-visible number, so a divergence is loud rather than
   discovered months later by a user typing an author's name.

Phase 0 alone makes search dramatically better without a single new field.

### 5.2 Phase 1 — the inversion

Struct-derived document, `search:"-"` opt-out, and the reflection test from §4.1.
Requires a full reindex, which Phase 0 has already built.

### 5.3 Phase 2 — query surface

Date support, full numeric ranges, aliases, and the prefix/fuzzy/quoted
consistency fix.

### 5.4 Phase 3 — transcripts

Gated on the measurement in §4.4.

---

## 6. Explicitly not claimed

- **The root cause of the missing index rows is not established.** §1.3 names two
  hypotheses and rules out neither.
- Index size after adding ~80 fields **has not been measured or estimated**. It
  must be measured before Phase 1 ships, not after.
- Reindex duration for the full library is unknown.
- No claim that the ABS browse path is *correct* — only that it returns 18 where
  search returns 1. Whether 18 is the right answer (versus 16 from the filter
  path, versus some collapse of version groups) is itself unresolved and overlaps
  the double-primary defect tracked in `todo.d/20260811-version-group-two-primaries.md`.
- Per-user search correctness is out of scope and remains an open design problem.

---

## 7. Related

- `todo.d/20260811-version-group-two-primaries.md` — why counts disagree between paths
- `docs/plans/2026-07-13-review-queue-and-regroup.md` — the shipped review queue
- #2268 — durable dirty-set + reconciler for the search index (stopped new drops;
  did not backfill old ones)
