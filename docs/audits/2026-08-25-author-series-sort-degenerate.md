<!-- file: docs/audits/2026-08-25-author-series-sort-degenerate.md -->
<!-- version: 2.0.0 -->
<!-- guid: 9d3f6b28-4c17-4e85-a0b9-7f2e5c1d8a64 -->
<!-- last-edited: 2026-08-25 -->

# 13 of 23 `sort_by` keys order nothing

## What was measured

Through the real entry point (`AudiobookService.GetAudiobooks`), on a Pebble
store with warmup complete. For each key, three books were seeded whose field
values rank 0,1,2 and were **inserted in the order 1,2,0** so that the three
possible outcomes are distinguishable strings:

| outcome | returned order |
|---|---|
| correct ascending | `[rank0 rank1 rank2]` |
| correct descending (inverted) | `[rank2 rank1 rank0]` |
| no ordering applied (insertion order) | `[rank1 rank2 rank0]` |

The first probe seeded 2,1,0, which makes "insertion order" and "correct
descending" the same string. That confound was removed before recording
anything below; `title` is the known-good control and passes.

## Result

```
title          ok                    [rank0 rank1 rank2]
narrator       ok                    [rank0 rank1 rank2]
duration       ok                    [rank0 rank1 rank2]
file_size      ok                    [rank0 rank1 rank2]
format         ok                    [rank0 rank1 rank2]
author         DEGENERATE(insertion) [rank1 rank2 rank0]
series         DEGENERATE(insertion) [rank1 rank2 rank0]
year           DEGENERATE(insertion) [rank1 rank2 rank0]
bitrate        DEGENERATE(insertion) [rank1 rank2 rank0]
genre          DEGENERATE(insertion) [rank1 rank2 rank0]
language       DEGENERATE(insertion) [rank1 rank2 rank0]
publisher      DEGENERATE(insertion) [rank1 rank2 rank0]
codec          DEGENERATE(insertion) [rank1 rank2 rank0]
quality        DEGENERATE(insertion) [rank1 rank2 rank0]
edition        DEGENERATE(insertion) [rank1 rank2 rank0]
sample_rate    DEGENERATE(insertion) [rank1 rank2 rank0]
```

Adding the aliases that share a comparator with a degenerate base key
(`bitrate_kbps`, `sample_rate_hz`) gives **13 of the 23 keys in
`bookSortComparators`**. `created_at`/`updated_at` were not probed (they are
auto-stamped); both are `BookSummary`-carried, like every key that passed.

The working set is exactly "fields `BookSummary` carries." The degenerate set
is exactly "fields it drops."

## Cause

`service_query.go:264` builds the candidate set with
`books = bookSummariesToBooks(summaries)`, and `:297` runs `applySorting(books, f)`
on it. `bookSummaryToBook` (`service_filtering.go:597`) copies only the summary
fields, so every comparator for a dropped field reads `""`/`0` for every row,
compares all-equal, and `sort.SliceStable` returns the input untouched.

The knowledge was present and correctly applied one branch earlier. The comment
at `service_query.go:277` says, of the *filter* pass:

> BookSummary doesn't carry every Book field (Language, Genre, Publisher,
> Edition, Codec, Quality, FingerprintStatus, CoveragePercent are all
> BookSummary-absent)

…and skips re-filtering for exactly that reason. Twenty lines below, the *sort*
pass runs on those same dropped fields. Nothing here is stale: verifying that
comment confirms it is true.

## Correction to v1.0.0 of this document

v1.0.0 claimed `Book.Author` is "never persisted" because of its `db:"-"` tag.
**That is wrong.** `db:"-"` governs the SQL tier; Pebble marshals the row as
JSON and the field is `json:"author,omitempty"`. Measured round-trip:

```
returned from CreateBook: Author=&{7 Zelda Zephyr}
PEBBLE READ-BACK: Author="Zelda Zephyr" sortValue="Zelda Zephyr"
```

The author is dropped for a different reason: `stripBookForMemdb`
(`memdb_strip.go:52-54`) nils `Author` and `Series` at both memdb insert points
(`memdb_sync.go:195`, `memdb_warmup.go:221`), so the memdb sort index for
`author` is built entirely from empty strings.

This distinction matters for the fix: the memdb `*Book` **does** still carry
genre, language, publisher, codec, quality, edition, year, bitrate and
sample_rate — only `Author`/`Series` are stripped. So 9 of the 11 degenerate
fields are visible to the store's own walker and only 2 need name resolution.

## Why no test caught it

- `TestProp_SortStability` / `TestProp_SortIsPermutation`
  (`audiobook_service_prop_test.go`) call `applySorting` **directly** on
  hand-built `database.Book` values, and deliberately populate `Author`/`Series`
  (line 100: "so the comparators aren't always comparing two empty strings").
  They never traverse the projection.
- Both properties — stability and permutation — hold perfectly when every
  comparator returns `""`. An all-equal comparison is stable, and is a
  permutation. The properties chosen cannot observe this class of defect.
- `TestSortIndexOrderMatchesComparator` compares the memdb index against the
  comparator. Both sides read the same nil pointer, so they agree on garbage.
