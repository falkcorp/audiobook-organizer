## Author-numbering cleanup follow-ups (from the 2026-09-05 production runs)

- [ ] `PebbleStore.DeleteAuthor` → `sweepAuthorFromBookAuthors` scans the whole
      `book_authors:` junction (~296k rows) per delete; 1,610 deletes took ~28 min.
      When memdb is warm, resolve the author's book ids from the in-memory index and
      delete only those junction keys (~40 lines + tests). Keep the full scan as the
      cold-memdb fallback.
- [ ] 3,372 books were left with no author row (`books-left-authorless` 896 + 2,476).
      After the bulk metadata fetch, measure how many still have none and decide
      whether they need the placeholder `Unknown Author` or a path-derived guess.
- [ ] 662 `out-of-scope` rows (publisher/copyright/translator shrapnel that fails
      `CleanAuthorNameForCreation` for a non-numbering reason) are untouched. Some
      name real people ("Alex A. Ryans - translator"). Needs its own classifier
      before any delete.
