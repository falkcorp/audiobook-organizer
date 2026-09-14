### Fixed

- Metadata applies no longer silently drop co-authors. A candidate carries one
  author, and every apply (auto-fetch, batch-apply-one, batch-apply-candidates,
  the review lane, the single apply) replaced the book's author credits with
  that one author, so a book credited to A and B came out credited to A alone.
  Applies are now fill-only: existing author links are kept, the candidate's
  author is added if missing, and the primary author is not repointed. Only a
  single apply that explicitly sends `replace_authors: true` replaces the
  credits. Author-join read/write errors are now returned instead of discarded.
