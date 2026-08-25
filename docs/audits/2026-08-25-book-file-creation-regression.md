<!-- file: docs/audits/2026-08-25-book-file-creation-regression.md -->
<!-- version: 2.0.0 -->
<!-- guid: 09f65679-4e03-4f95-89a2-07c015489f11 -->
<!-- last-edited: 2026-08-25 -->

# `book_file` row creation regressed between 2026-08-11 and 2026-08-14

**Measured 2026-08-25 against production (61,455 book rows) while a `library.scan`
was running.** A book row with no `book_file` rows has no route to any audio, so
this is the mechanism behind "new books get added but I can't listen to them".

A census puts it at **12,525 books with no `book_file` rows — 20.4% of the library.**

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


## ROOT CAUSE (found 2026-08-25, after the first version of this document)

**A single production configuration value: `chapter_consolidation_threshold_min = 0`.**
The intended default is `10`. `0` means "disable consolidation" — that is documented
behaviour of the field, not a bug in itself.

Verified three ways:

| leg | evidence |
|---|---|
| the guard | `internal/scanner/chapter_consolidation.go:50-54` — `if thresholdMin <= 0 { return filesToBooks(files) }`, i.e. **one Book per file, no `SegmentFiles`** |
| the live value | `GET /api/v1/config` -> `chapter_consolidation_threshold_min = 0` (`root_dir = /mnt/bigdata/books/audiobook-organizer`) |
| the intended default | `viper.SetDefault("chapter_consolidation_threshold_min", 10)` at `config.go:1392`, and `config.go:811` declares the field with **no `omitempty`** |

### The chain

1. A multi-file book's files mostly carry **no album tag**. Measured on the book found
   ping-ponging in the production log (`01KZR9D70KWZH8HG1M4G3SRCWE`,
   `.../Unknown Author/Star Wars_ Darth Plagueis/`): **223 of 224 mp3s have no album
   tag**; track numbers are present. With no album they cannot be grouped by album —
   correct behaviour — so they fall into `noAlbum`.
2. `noAlbum` is handed to `consolidateChapterGroups` (`scanner.go:2556`), which is the
   last chance to reassemble them into one book.
3. With the threshold at `0` that function returns immediately: **one Book per file**.
   The ">= 3 files sharing a numbered base title" logic never runs.
4. Each such Book is a FILE path arriving at site 1487 with `len(SegmentFiles) == 1`,
   so the `if len(books[idx].SegmentFiles) > 1` gate is false and
   **`createBookFilesForBook` is never called at all.** Zero `book_file` rows,
   deterministically.
5. Because each file also became its own book row, the same value produces the
   track-titled fragment rows.

One config value produces **both** populations described below.

### Independent corroboration from the production log

Read with the exact allowed command (`sudo /usr/bin/journalctl -u
audiobook-organizer.service`; adding `--since` or `--no-pager` breaks the NOPASSWD sudo
rule and returns nothing). A 2-minute window of the live scan:

- **117** `scanner: re-linking book` events, **96 of them for the same book id**, each
  repointing that one row to a different track (`07_10-...mp3`, `07_13-...mp3`,
  `08_01-...mp3`).

That is one multi-file book being processed as ~96 separate books. It is also
creation-side evidence that touches **no read endpoint**, which settles
"rows are missing" versus "rows are hidden by a read filter" in favour of missing: a
read filter cannot explain a log line that was never written.

### Scale (census, not a sample)

`GET /api/v1/maintenance/book-file-hash-stats`, cross-checked against
`maintenance/acoustid-stats` which independently reports the same `total_files`:

| | |
|---|---|
| total books | 61,490 |
| **books with no `book_file` rows** | **12,525 (20.4%)** |
| total `book_file` rows | 545,932 |

Caveats carried from the session that produced it: it is not verified whether
`books_with_no_files` excludes soft-deleted rows, and 20.4% library-wide is **not** the
post-boundary rate — it is diluted by a healthy pre-boundary majority.

### What is still NOT established

**When and how the value became `0`.** `/var/lib/audiobook-organizer/config.yaml` is 724
bytes with mtime `2026-08-24T01:24:08` — after the boundary, so it dates the last write,
not the flip. The file is `0600` owned by `audiobook` and `sudo cat` is not in the
NOPASSWD allowlist, so its contents could not be read. That it is only 724 bytes means it
is a **partial** config; combined with the missing `omitempty`, a write from a
partially-populated struct would persist a hard `0` that then beats viper's default on
every subsequent load. That is a plausible mechanism, **not a demonstrated one**.

### Fixing it

Setting the value to `10` fixes **future scans only**. The 12,525 books already written
without `book_file` rows, and the fragment rows, are existing damage needing a separate
repair. Production config is not changed by this document.

Worth treating as a defect in its own right: a partial config write can silently and
permanently disable a grouping path with **no log line and no startup warning**. The
absence of any signal is why this ran for eleven days behind a green test suite.

## Mechanisms considered on the way, and how each resolved

Recorded because each is plausible enough to be re-proposed, and because two of the
three were confidently believed before a control killed them.

**Not duplicate rows starving each other.** 1,714 file paths carry more than one book
row (5,313 rows). Of today's orphaned books, 11 of 13 had a peer row at the same path
holding the files, which looks like a starvation mechanism: `GetBookByFilePath`
returns one row, `len(existing) > 0` returns early, the twin never gets rows.
**Refuted by control.** Duplicate groups in which *both* rows predate the boundary:
**59 of 60 have all rows holding files, 1 split.** Duplicates coexist normally. Every
peer holding files was created pre-boundary; every orphan post-boundary. Duplication
is chronic and flat across the boundary (April 8.1% duplicate share, August 9.5%) and
cannot produce a 90-100% failure rate.

**The `len(SegmentFiles) > 1` gate — eliminated for directory books, but it IS the
mechanism for file-path books.** This was scoped too narrowly in v1 of this document.
A book whose path is a *directory* takes the branch at `scanner.go:1224` and reaches
site 1285 unconditionally, so the gate cannot explain that population — that part
stands. But the far larger population arrives as *file* paths (one per track, because
consolidation is disabled), hits the gate at site 1487, and gets **no call at all**.
The gate is longstanding and unchanged in the window; what changed is the config that
started feeding it single-file books. #2926 fixes the gate's single-file case at save
time but is not deployed.

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
