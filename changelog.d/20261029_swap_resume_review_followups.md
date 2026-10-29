### Fixed

- Resuming an unfinished version swap no longer gets stuck on a book whose two
  versions' files share a name. When a swap failed after putting its files back
  but before restoring the entering version's status, the active version's file at
  the book's folder was taken for the entering version's, the resume refused
  ("is at both"), and the book could be neither resumed, trashed nor swapped.
- A resume no longer moves a file it cannot prove is the version's: a file found
  where the swap would not have put it must match the row's recorded size or hash,
  and a row that recorded neither is refused. Every spelling of a path, and the
  path an undo moves a file to, is checked for another row; each moved file gets a
  `version_swap_resume_file` path-change record written before the move; a path
  lookup that cannot be completed is a 500, not a 409 refusal.
- Resuming a swap that stopped at its status writes (only the entering version left
  `swapping_in`) now tells Deluge to follow it, and the answer is sent before Deluge
  is called.
- The trash cleanup's log no longer reads "0 failed" beside an error when only the
  list of unfinished swaps could not be read, and the Unfinished swaps confirmation
  says the server works out what to do again when it is confirmed.
