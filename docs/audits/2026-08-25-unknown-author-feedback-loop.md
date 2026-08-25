<!-- file: docs/audits/2026-08-25-unknown-author-feedback-loop.md -->
<!-- version: 3.0.0 -->
<!-- guid: 7a1c9e2f-4b83-4d16-9f52-c8e0a7d31b64 -->
<!-- last-edited: 2026-08-25 -->

# "Unknown Author" is a self-perpetuating author, not a placeholder

**Measured 2026-08-25 against production (61,412 books).**

## The headline

`"Unknown Author"` is not an empty value that the system knows to fill in later.
It is a **materialized author row** (`author_id = 54846`) whose name organize
also **bakes into the filename**. Every downstream check that asks "does this
book have an author?" answers **yes**, so the library cannot heal these books,
and powering the LLM host back on will not change that.

**The gated population is 3,407 books, not 25,304.** An earlier draft of this
document led with 25,304 and that number does not mean what it looks like — see
the next section before quoting either figure.

## Measurements, and what they do and do not mean

| Query | Count |
| --- | --- |
| `GET /audiobooks` (no filter) | 61,412 |
| `search=ZZQQBOGUSXYZ` (bogus control) | 0 |
| `search=ZZQQBOGUS Author` (OR control) | 0 |
| `search=Pratchett` | 685 |
| `search=Unknown Author` | 25,304 |
| **`author_id=54846`** | **3,407** |

Instrument notes, because two of these are traps:

- **`q=` is inert.** It returns the full 61,412 for any value, including
  nonsense. Never count with it. `search=` is the live filter: the bogus
  control returns 0 and the unfiltered baseline returns 61,412.
- **`search=` AND-combines tokens; it is not a phrase match.** `ZZQQBOGUS Author`
  returns 0 (so tokens are AND'd, not OR'd), but `Author Unknown` returns the
  same 25,304 as `Unknown Author` (so order is not significant).

**The 25,304 is dominated by a stale path, not a broken author.** Sampling the
matched set at two different offsets gives very different pictures:

| Sample | at `author_id=54846` | typical `author_name` of the rest |
| --- | --- | --- |
| offset 0, n=1000 | 237 (24%) | junk: `19 - Apocalypse`, `Rings Haven`, `?` |
| offset 12000, n=600 | 25 (4%) | **correct**: `Michael Chatfield`, `M. R. Forbes` |

The offset-12000 rows look like this — a correct author on a row whose file is
still parked under the placeholder directory:

```
author_name = "Michael Chatfield"
file_path   = /mnt/bigdata/books/audiobook-organizer/Unknown Author/Sixth Realm, Part 2/...
```

Those books are catalogued correctly and merely **misfiled on disk**: organize
ran while the author was unknown, the author was resolved later, and nothing
re-organized the file. They need a re-organize, not an AI re-parse, and they are
not part of the defect below.

Because the population is heterogeneous and ordering-dependent, **the sample
proportions above must not be extrapolated.** The only whole-population figure
that is safe to quote is `author_id=54846` = **3,407**.

A separate, real, and so-far **unquantified** problem is visible in the offset-0
sample: author rows minted out of filename fragments — `19 - Apocalypse`,
`18 - Ascension`, `2 - The Old Republic Revan`, `Rings Haven`, `?`. These are
title/track fragments that `extractInfoFromPath`'s `" - "` split promoted to
authors. Every one of them is a non-nil `AuthorID` and therefore closes the same
gate. Their whole-population count is not measured here.

A representative placeholder row:

```
id          = 01KZRB0D90QNXHYR88ER2SX05C
title       = "Pratchett 036 - Unknown Author"
author_id   = 54846
author_name = "Unknown Author"
file_path   = /mnt/bigdata/books/audiobook-organizer/Unknown Author/Pratchett 036/Pratchett 036 - Unknown Author.mp3
authors     = [ {38566 "Terry Pratchett" author}, {47587 "Carpe Jugulum" author} ]
```

Note the last line: **the join slice already carries the correct author.** The
scalar `book.AuthorID` points at the placeholder while the join slice holds
"Terry Pratchett". Repairing the author does not require the LLM.
(`Carpe Jugulum` is a book title mis-ingested as an author — the same
fragment-to-author defect noted above.)

## The loop, in three sites

1. **Organize bakes the placeholder into the path and the filename.**
   `internal/organizer/organizer.go:376` — `const placeholderAuthor = "Unknown Author"`,
   used as `AuthorFallback`. The book lands at
   `.../Unknown Author/Pratchett 036/Pratchett 036 - Unknown Author.mp3`.

2. **The next scan reads it back as a real author.**
   `extractInfoFromPath` splits the stem on `" - "` and takes the right-hand side
   as the author. Verified by direct probe against the real production path:

   ```
   PROBE Title="Pratchett 036" Author="Unknown Author" Series=""
   ```

   The placeholder is now indistinguishable from metadata a human supplied.

3. **The AI nomination gate then refuses to re-parse the book.**
   `internal/scanner/scanner.go:1413` —

   ```go
   if dbExisting.Title != "" && dbExisting.AuthorID != nil && *dbExisting.AuthorID != 0 {
       needsAI = false
   }
   ```

   `Title` is `"Pratchett 036 - Unknown Author"` (non-empty), `AuthorID` is
   `54846` (non-nil, non-zero). The gate closes. **These books are permanently
   excluded from AI re-parse.**

Even if the gate were opened, `runAIBatchPhase` only fills fields that are
**empty** (`if books[idx].Author == "" && aiMeta.Author != ""`), and site 2 has
already set `Author = "Unknown Author"`. The AI's answer would be discarded.
Any fix must address the sites together; fixing the gate alone is inert.

## What this does NOT explain

The LLM host (`<llm-host>:11434`) is **unreachable from production** — `curl`
exit 7, 100% ICMP loss, measured from the production server itself, not merely from a
workstation. That is a separate, live outage and the cause of the
`context deadline exceeded` / `0/45 book(s) parsed ... ABORTED` log lines. It
needs the host powered on. The abort path does **not** stamp the scan cache
(the stamp is written only inside the success branch of `runAIBatchPhase`), so
books missed by the outage alone are correctly re-nominated on the next scan —
*unless* they are also caught by the loop above, which is the point of this
document.

## Consequence for the repair

The repair of the shattered per-track rows must clear the placeholder author
(or reconcile the scalar `AuthorID` against the join slice) as part of the same
change. A repair that merges 80 fragment rows into one book but leaves the
merged row pointing at author 54846 produces a book that is *permanently*
unparseable by every self-healing path in the system.

**The cheapest repair primitive is a scalar-vs-join reconciliation, not an AI
re-parse.** The join slice already holds the right answer for at least some of
these rows (`Terry Pratchett`, id 38566, on a row whose scalar points at 54846).
That repair needs no LLM, no re-scan, and no filename heuristics — it reads a
value the database already stores. It is cross-lane (`internal/database`,
`internal/merge`) and belongs in the repair design, not here. Its coverage —
how many of the 3,407 have a usable join slice — is **not yet measured**.

## Two placeholder rows, not one — and why that nearly made the fix inert

There are **two** author rows named `Unknown Author`:

| id | book_count |
| --- | --- |
| 54845 | 0 |
| 54846 | 2,128 |

`GetAuthorByName` resolves through an `author:name:<normalized>` index that maps
one normalized name to exactly **one** id, so it can only ever return one of
them. The first version of this fix compared the row's `AuthorID` against that
single resolved id — which guards one row and leaves every book under the other
permanently gated. Had the index named the empty row, **the entire fix would
have been inert in production while every test passed.**

The check now resolves the author row's own name via `GetAuthorByID` and asks
`authorname.IsPlaceholder`, so it is correct for any number of duplicate rows.
`TestPlaceholderAuthorsResolvesByNameNotByTheNameIndex` pins it, and a mutant
restoring the id comparison fails it.

## `CreateAuthor` is check-then-create with no atomicity

This is how the duplicate rows arise, and it is a defect in its own right.

`PebbleStore.CreateAuthor` calls `GetAuthorByName` and, on a miss, mints a new
row. Two callers with the same name can both miss. Measured 2026-08-25 with a
direct probe: **24 concurrent `CreateAuthor` calls with an identical name
produced 24 distinct author rows**, reproducibly across three runs — not an
occasional race, a near-total failure of the dedup check under concurrency.

The scanner resolves authors from inside its worker pool, so a library import
that first encounters an author on several books at once mints a row per worker.
This is a plausible contributor to the 17,947 author rows and is consistent with
the previously-recorded finding that ~212 authors' books carry a dangling
`AuthorID`.

It lives in `internal/database` and is **not fixed here** — filed as
`todo.d/20260825-createauthor-check-then-create-race.md`. The fix needs the
lookup and the insert in one atomic batch, plus a decision about merging the
duplicate rows already present.

## Junk authors: measured

The fragment-derived authors noted above are no longer unquantified. Classifying
all 17,947 author rows into disjoint tiers:

| tier | authors | books |
| --- | --- | --- |
| leading track/volume number (`19 - Apocalypse`) | 1,720 | 1,199 |
| the `Unknown Author` placeholder | 2 | 2,128 |
| bare number (`07`) | 2 | 1 |
| punctuation / entity only (`&#169`, `?`) | 29 | 26 |
| trailing number (`Avatars Dance 1`, `Book 1`) | 2,890 | 1,212 |
| **total** | **4,643** | **4,566** |

**25.9% of all author rows are not people.** They account for 4,566 of the
53,790 attributed books (8.5%). Every one of them is a non-nil `AuthorID` and so
closes the same nomination gate the placeholder did — the fix here addresses only
the placeholder tier (2,128 books); the other ~2,438 remain gated behind authors
that are really title and track fragments.

The tiers are conservative and pattern-based, so treat them as a floor: a name
like `Rings Haven` is a mis-parsed title but matches no pattern here and is not
counted.

## Scope note: why site 1 is deliberately not fixed

Sites 2 and 3 are scanner bugs: the scanner reads back a value the system itself
wrote and then treats it as user-supplied truth. Those are fixable in place.

Site 1 is not a bug in the same sense. Organize needs *some* directory name for
a book with no resolvable author, and a placeholder is a defensible choice.
Changing it alters on-disk naming policy for every future organize and would
move files that are already on disk. That is a user decision with physical blast
radius, not autonomous flaw-fixing, and it is deliberately left alone here.
