### Fixed

- Saving a book edit now writes the book row, its author list (book_authors) and
  its narrator list (book_narrators) in one write under the book's write lock
  (`ModifyBookCredits`). Before, the two lists were written after the row
  committed and outside the lock, so a concurrent narrator sync of the same
  book could have its credits overwritten, or overwrite the edit's, and leave
  the narrator field and the narrator list on different casts. A list the edit
  does not change is no longer rewritten at all, so a title-only save cannot
  re-order credits, and an edit that changes no list writes the row alone, so a
  corrupt credit list no longer blocks it. A credit write that fails, or a
  narrator that cannot be resolved or created, now fails the edit instead of
  returning a warning after a half-saved edit. Other writers (the metadata
  apply) still write the row and the lists as separate steps.
- `ModifyBookCredits` stages only the credit lists its callback changed, and
  bumps the library generation after mirroring them into memdb so a cached
  author page is not served stale.
