## The "Unknown Author" repair is two populations, and only one is cheap

Measured 2026-08-25 against production (complete scalar census of all 61,447 book
rows; full detail in `docs/audits/2026-08-25-unknown-author-feedback-loop.md`).

The 3,598 books whose scalar `author_id` is the placeholder split cleanly:

| bucket | count | share | route |
| --- | --- | --- | --- |
| join slice already holds a usable author | **1,291** | 35.9% | DB-only reconciliation |
| nothing local holds the author | **2,307** | 64.1% | external lookup by title |

For the 2,307, every local source is exhausted — verified, not assumed:

- `original_filename` is empty for **97.3%** of a 300-row random sample
- **100%** sit under a literal `Unknown Author/` directory
- embedded tags: **0 of 60** carry an artist/album_artist/composer value,
  against a known-good twin of **30 of 30** on ordinary books with the identical
  `ffprobe` command

So the name is not mislinked, it is gone. Re-scanning cannot recover it,
re-parsing the filename cannot recover it, and an AI pass over filenames cannot
recover it — there is nothing left to parse.

### Task 1 — repair the 1,291 (unblocked, do this first)

Reconcile the scalar `book.AuthorID` against the join slice where the slice holds
a non-placeholder, non-junk author.

- Must **REPOINT**, never delete: `DeleteAuthor` does not sweep `book.AuthorID`,
  which is how ~212 books already carry a dangling one.
- Must not leave the merged row pointing at 54846 — a repaired row that keeps the
  placeholder is permanently unparseable by every self-healing path.
- Dry run first, with per-bucket counts, before any write.
- Lane: `internal/database` / `internal/merge`.

### Task 2 — decide the route for the 2,307 (needs a decision, not code yet)

Candidates, in rough order of expected yield:

- **External metadata lookup by title** (Open Library / Audible). The only route
  with real coverage, and the enrichment machinery already exists.
- **LLM pass over TITLES, not filenames.** A minority of titles embed the author
  inline — `Starship's Mage Book 14 Glynn Stewart Chimera's Star`. Worth doing,
  but it is a different operation from filename parsing and must not be reported
  as the same one.
- **Leave them.** They are catalogued and playable; only the author is unknown.

Do not start Task 2 before Task 1 — Task 1 is free and shrinks the problem by a
third.

- [ ] Dry-run the 1,291 reconciliation and report per-bucket counts
- [ ] Apply it, REPOINTING rather than deleting
- [ ] Decide the route for the 2,307
- [ ] Re-census afterwards to confirm the cohort actually shrank
