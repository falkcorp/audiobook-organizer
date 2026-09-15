### Fixed

- Nine HTTP handler book writes no longer revert fields another writer saved
  while the request was in flight. Each read a book, changed one or two
  columns and wrote the whole stale row back; each now sets only its own
  columns inside `ModifyBook`, under the book's write lock, and writes nothing
  when the row already has the value:
  - `POST /audiobooks/:id/organize` (in-place landing) stamps
    `LastOrganizeOperationID` / `LastOrganizedAt` on the original book. The
    stamp on the row `CreateOrganizedVersion` just created stays a plain
    `UpdateBook`: it is that row's first write.
  - `POST /audiobooks/:id/clear-no-match` clears `MetadataReviewStatus`; a
    missing book is a 404 and a store error is now a 500 rather than a 404.
  - `POST /diagnostics/apply-suggestions`: `delete_orphan` sets
    `MarkedForDeletion`; `fix_metadata` and `reassign_series` read the user's
    field locks first and apply the suggestion to the stored row, still
    skipping a book whose locked field is the one the suggestion targets.
  - `POST /audiobooks/:id/reconcile-files` writes the `FileSize` aggregate.
  - `POST /audiobooks/:id/relocate` writes the book's `FilePath`.
  - Author split / reclassify (`repointPrimaryAuthor`) sets `AuthorID` and
    the denormalized `Author`. Its fallback that wrote a `BookCore`
    projection as the whole row when the hydrate read failed is gone: the
    store reads the row itself now, and that projection would have blanked
    every column `BookCore` does not carry. A book whose primary author was
    already repointed by someone else is left alone.
  - `POST /series/:id/split` moves `SeriesID`; a book no longer in the old
    series is not counted as moved.
- The four narrowed handler store interfaces (`OrganizeStore`,
  `MetadataCacheBookStore`, `AudiobookBookStore`, `BookEntityStore`) gain
  `ModifyBook`; the generated mocks were regenerated.
