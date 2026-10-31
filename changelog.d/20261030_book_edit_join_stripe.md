### Fixed

- Saving a book edit now writes the book row, its author list (book_authors) and
  its narrator list (book_narrators) in one write under the book's write lock
  (`ModifyBookCredits`). Before, the two lists were written after the row
  committed and outside the lock, so a concurrent metadata apply or narrator
  sync of the same book could have its credits overwritten, or overwrite the
  edit's, and leave the narrator field and the narrator list on different
  casts. A list the edit does not change is no longer rewritten at all, so a
  title-only save cannot re-order credits. A credit write that fails now fails
  the edit instead of returning a warning after a half-saved edit.
- `ModifyBookCredits` stages only the credit lists its callback changed, and
  bumps the library generation after mirroring them into memdb so a cached
  author page is not served stale.
