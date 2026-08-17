### Compound narrator names are not split into individual narrators

A book narrated by two or three people is stored as one narrator whose *name* is
the whole credit string — "Michael Kramer & Kate Reading" is a single narrator
record, not two. Filtering, faceting, and "more by this narrator" therefore miss
every multi-narrator book, and the narrator list is polluted with entries that are
not people.

The schema is not the problem. `BookNarrator` (`internal/database/store.go:107`) is
a proper many-to-many join, `SetBookNarrators` exists, and `NarratorsJSON`
(`store.go:253`) carries a second tier into the summary projection. Nothing needs
migrating — the rows just never get created.

**Where it goes wrong: nothing splits at ingest.** `internal/metadata/metadata.go:266-269`
reads `PERFORMER` / `TXXX:NARRATOR` / `©nrt` into a single `metadata.Narrator`
string, runs `cleanTagValue` over it, and stores it whole. There is no split on that
path, so every scan and every metadata apply writes compound names straight through.

**The splitter that does exist is in the wrong place and too narrow.** The only
narrator-splitting code in the repo lives inside `OptimizeDatabase`
(`internal/server/handlers/operations/handler.go:276`, reporting a `narrators_split`
count). Three problems with relying on it:

1. **It is a manual maintenance op, not a rule.** It repairs history when someone
   runs it; the next scan re-introduces compound names immediately. Splitting
   belongs on the ingest path, with the op kept only as a backfill for old rows.
2. **It only splits on `" & "`.** `splitMultipleNames`
   (`internal/audiobooks/service_filtering.go:1086`) is `strings.Split(name, " & ")`
   and nothing else — so `"Kate Reading, Michael Kramer"`, `" and "`, `";"` and
   `" with "` are all left as one name. Comma-separated credits are the common case
   in real tags.
3. **It leaves two sources of truth.** `book.Narrator` keeps the compound string
   after the join rows are written, so callers reading the scalar and callers
   reading `book_narrators` disagree. Decide which is authoritative and say so.

Also note it walks `GetAllBooksCore(0, 0)` — the entire library in memory — in a
plain sequential loop, which is the shape CLAUDE.md's concurrency rule calls out. If
this gets promoted to a real backfill it needs a bounded worker pool.

**Suggested shape:**

- Split at ingest, in `internal/metadata`, so scans and applies both benefit.
- Widen the separator set beyond `" & "`: comma, `" and "`, `";"`, `" with "`, and
  narrator-specific noise like a trailing "(Narrator)". Be conservative — a name
  containing a comma ("Smith, John") must not be shredded, so prefer an explicit
  separator list over a generic tokenizer, and add fixtures for the ambiguous cases.
- Keep `OptimizeDatabase`'s pass as a one-time backfill for existing rows, using the
  same splitter rather than a second copy of the logic.
- Decide and document whether `book.Narrator` or `book_narrators` wins.
