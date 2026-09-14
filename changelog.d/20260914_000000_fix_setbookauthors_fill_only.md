### Fixed

- Metadata applies no longer silently drop co-authors. A candidate carries one
  author, and every apply (auto-fetch, batch-apply-one, batch-apply-candidates,
  the review lane, the single apply) replaced the book's author credits with
  that one author, so a book credited to A and B came out credited to A alone.
  Author apply is now add-only on every path, the hand-picked single apply
  included: existing author links are kept, the candidate's author is added if
  missing, and the primary author is not repointed. Removing an author is a
  manual edit. The add is one atomic read-merge-write in the store
  (`ModifyBookAuthors`), so two applies to the same book at once both keep
  their author. Author-join read/write errors are returned instead of
  discarded, and an added credit gets a change-history row so it can be undone.
