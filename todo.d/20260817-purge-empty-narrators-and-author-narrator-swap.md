### Contributor data cleanup — follow-ups to `maintenance.purge-empty-authors`

- [ ] **Narrator equivalent of the empty-author purge.** There is no
  `DeleteNarrator` on the store at all — narrators live at `narrator:<id>` with no
  delete path, so the op cannot be written until that exists. Scope it alongside
  whatever decides the narrator identity question below.
- [ ] **Decide what the 822 zero-book-but-has-files authors actually are.** Measured
  2026-08-17: of 4,975 zero-book authors, 4,153 also have zero files (unambiguous
  junk, purgeable today) and 822 have files. A zero book count with files present
  looks more like a book that lost its junction entry than an empty author, so the
  purge op holds them back by default (`require_zero_files`). Someone has to look at
  a sample and decide before that flag is ever flipped.
- [ ] **Author↔narrator swap repair.** Measured lower bound: 1,052 names appear in
  BOTH the author and narrator tables; 67 of those are swap-shaped (narrates ≥5
  books, "authors" 1–2), accounting for ~96 book-author links. Ray Porter, Scott
  Brick, Nick Podehl and Andrea Parsneau all currently exist as authors. This is a
  LOWER BOUND — the rule only sees names present in both tables, so a swap whose
  "author" never appears as a narrator elsewhere is invisible to it. Route any
  repair through the review queue rather than blind-applying; this is far smaller
  than it looks from the UI, where the impression is driven mostly by the empty
  authors and (until #2512) the compound narrator entries.
- [ ] **`DeleteAuthor`'s junction cleanup is dead code.** It iterates the
  `book_author:` keyspace (singular). Nothing in the repo writes that keyspace — the
  live data is the per-book `book_authors:<bookID>` array — and the iterator bounds
  (`book_author:` → `book_author;`) exclude the plural form anyway. So deleting an
  author who HAS books leaves them referenced inside every `book_authors` array.
  Harmless for the empty-author purge (no references by definition), a real bug for
  any other caller.
