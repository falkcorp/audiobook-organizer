<!-- file: docs/audits/2026-08-25-book-file-creation-regression.md -->
<!-- version: 1.0.0 -->
<!-- guid: 09f65679-4e03-4f95-89a2-07c015489f11 -->
<!-- last-edited: 2026-08-25 -->

# `book_file` row creation regressed between 2026-08-11 and 2026-08-14

**Measured 2026-08-25 against production (61,455 book rows) while a `library.scan`
was running.** A book row with no `book_file` rows has no route to any audio, so
this is the mechanism behind "new books get added but I can't listen to them".

Roughly **13,000 books created since 2026-08-14 are in this state.**

## The boundary

Sampled by `created_at` **day**, n=30/day. Sampling by page offset is wrong here —
it conflates storage position with creation date and yields a window two days too
wide.

| day | pool | sampled | no `book_file` rows |
|---|---|---|---|
| 2026-08-11 | 16,091 | 30 | **0.0%** |
| 2026-08-14 | 16 | 16 | 93.8% |
| 2026-08-15 | 33 | 30 | 96.7% |
| 2026-08-16 | 5,706 | 30 | 90.0% |
| 2026-08-17 | 2,300 | 30 | 100.0% |
| 2026-08-24 | 5,050 | 30 | 90.0% |
| 2026-08-25 | 43 | 30 | 90.0% |
| **control** 2026-04-04 | 31,140 | 30 | **0.0%** |

2026-08-11 sits at 0.0% over a **16,091-row pool**, so the healthy side is a strong
anchor rather than a small-sample artifact. 2026-08-12 and 2026-08-13 have **no rows
at all** — a two-day gap immediately before the collapse, which points at a deploy or
configuration change rather than code that silently rotted.

## What the mechanism is not

Three candidate mechanisms were tested and eliminated. Recording them so they are not
re-proposed.

**Not duplicate rows starving each other.** 1,714 file paths carry more than one book
row (5,313 rows). Of today's orphaned books, 11 of 13 had a peer row at the same path
holding the files, which looks like a starvation mechanism: `GetBookByFilePath`
returns one row, `len(existing) > 0` returns early, the twin never gets rows.
**Refuted by control.** Duplicate groups in which *both* rows predate the boundary:
**59 of 60 have all rows holding files, 1 split.** Duplicates coexist normally. Every
peer holding files was created pre-boundary; every orphan post-boundary. Duplication
is chronic and flat across the boundary (April 8.1% duplicate share, August 9.5%) and
cannot produce a 90-100% failure rate.

**Not the `len(SegmentFiles) > 1` gate, for directory books.** Site 1487 in
`ProcessBooksParallel` is gated, so a book arriving there with <= 1 segment gets no
call at all — that is real, and it is what #2926 fixes at save time. But it does not
explain this population: a book whose path is a directory takes the branch at
`scanner.go:1224`, where site 1285 calls `createBookFilesForBook(dirPath, nil, ...)`
**unconditionally**. The only earlier exit is `firstFile == ""`, which returns *before*
`saveBook`, so such a book would have no row at all — and these books have rows. For
directory books the call is made and fails inside.

**Not an outright `book:path:` index break.** `GetBookByFilePath`
(`pebble_store.go:1316`) is a pure index lookup, and `createBookFilesForBook` calls it
unconditionally before every other branch, so a broken index would give 0% success.
Today 6 of 43 books did get rows. The lookup demonstrably still works for some books
on the current binary.

## Unexplained, and probably a second defect

The successes are **partial**: Axiom 52 files on disk -> 42 rows; Foundation 149 -> 76;
Flux 59 -> 48; Omega Force 28 -> 28 (exact). A gate or a failed lookup explains
zero-versus-nonzero. Neither explains 149 -> 76. This is flagged as unexplained rather
than folded into whichever mechanism is settled on.

## How to test a candidate mechanism

Any repro must **discriminate between "no call was made" and "the call was made and
returned early"** — both produce zero rows and only the mechanism differs. A test that
asserts only on the end state will pass against the wrong fix. Asserting on whether
`GetBookByFilePath` was called at all separates them.

Prefer a local failing test over a production counter: a counter needs a deploy, and at
time of writing production runs a binary from `2026-08-24 23:26:31` that predates
#2926 and #2927.

## Measurement traps hit while producing this

1. **`/api/v1/operations` is 404; `/operations/active` is 410 Gone.** Parsing the error
   envelope with `.get("operations") or []` yields a confident "0 running operations"
   while a scan is running. Use `GET /api/v1/operations/timeline?limit=N`, read
   `d["data"]["operations"]`, and assert `"error" not in payload` before trusting a zero.
2. **The audiobooks list `sort` param is inert.** `order=desc`, `order=asc` and
   `sort=NOT_A_FIELD_XYZ` return the identical page. Every aggregate here is computed
   client-side over all paged rows.
3. **Prefix matches need a control.** Validated with a bogus prefix (0 rows) paired
   with a known-good twin (`/mnt/bigdata/books` -> 61,440 rows).
4. **Repairing an author via `author_name` replaces the join slice.** That branch
   (`internal/audiobooks/service_mutation.go:150`) calls `SetBookAuthors` and resolves
   through `CreateAuthor`, splitting only on `" & "`. Sending `author_id` alone takes
   `service_mutation.go:87` and leaves the join slice untouched — verified by a
   single-row round-trip in which `author_id`, `author_name`, `author` and `authors[]`
   all agreed afterwards.
