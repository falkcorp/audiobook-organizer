<!-- file: changelog.d/20260804_100000_repair_junk_titles.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9c4e7a12-3b58-4d06-8f21-7ae5c0d94b63 -->
<!-- last-edited: 2026-08-04 -->

### Added

- **`maintenance.repair-junk-titles`** — recovers book titles that were replaced by
  something that is demonstrably not a title. A library scan found **2,061 such books**:

  | stored title | count | cause |
  |---|---|---|
  | `read by narrator` | 1,595 | importer kept the filename's trailing credit instead of the leading title |
  | `big finish ident` | 170 | track 1's ID3 tag promoted to the book title |
  | `opening credits` | 152 | same |
  | `intro` | 144 | same |

  These are **real, full-length books with the wrong name** — 30.71h, 9.62h, 27.32h —
  not junk records. The existing `dedup-books` Phase 1 deliberately deletes only those
  with no author, series, ISBN or description, so these survive it and must be
  *retitled*, never deleted.

  `maintenance.title-repair` does not reach them either: a dry run against production
  would have fixed **7 books**, skipping 34,912 as single-file and 3,742 for
  "no agreement" — its all-chapters-agree check cannot work when the tracks
  legitimately disagree ("Track 01", "Track 02"…).

  Two evidence sources, chosen by shape:

  - **Multi-file → the folder.** The organizer named these correctly; only the title
    field is wrong: `.../Big Finish Productions/Dark Gallifrey/Dark Gallifrey - The War Master Part 2`.
  - **Single-file → the filename**, parsed for the
    `<Title> - <Author> - read by <narrator>` convention. Their folder is poisoned by
    the same bad title (`.../Nocturne/read by narrator/`) and is useless.

  🔑 The author name is what makes the filename parse safe. Naively dropping the last
  `" - "` segment after the credit corrupts any title that legitimately contains one —
  `"Dark Gallifrey - The War Master Part 2 - read by narrator.mp3"` would become
  *"Dark Gallifrey - The War Master"*. The author is stripped only when it actually
  matches, so with no author known only the credit is removed.

  The op **refuses rather than guesses**: no evidence means the book is left alone,
  because a known-bad title is still detectable later while an invented one is not. It
  never swaps one junk title for another, never writes a result under two characters,
  and honours user overrides, fetched values and provider-applied metadata exactly as
  `title-repair` does. Dry-run by default, reporting old → new and which evidence was
  used. Parallel by book; only the ~2k junk-titled books pay the per-book reads.

  15 tests on the derivation, including the two corruptions an earlier draft actually
  produced: an unbounded parent-directory walk that returned `"lib"` for
  `/lib/A/intro.mp3` and the author's name for `/lib/Author/Some Book/Some Book.m4b`.
