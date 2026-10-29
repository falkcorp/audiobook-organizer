### Added

#### Books whose version swap did not finish can be found and resumed

A swap that moved a book's files but could not finish writing the records
(`versions.ErrSwapIncomplete`, the 500 `VERSION_SWAP_INCOMPLETE`) left the
versions in `swapping_out` / `swapping_in`, the book with no active version, and
nothing that listed it. Now:

- `GET /api/v1/audiobooks/swap-incomplete` lists those books (book path, each
  swapping, active and alt version with its file rows, and what a resume would do
  or why it would refuse). Version Management has a third tab, "Unfinished swaps",
  with a Resume button per book.
- `POST /api/v1/audiobooks/:id/versions/resume-swap` (`library.organize`, under the
  merge lock like the swap) runs `versions.ResumeVersionSwap`. It classifies every
  file row by the disk (book folder or its own `.versions/{id}/` slot), finishes the
  swap when no file needs to move, otherwise moves the files back without replacing
  anything and makes the leaving version active again, and records the move as a
  `version_swap_resume` path change. It refuses with 409 `SWAP_RESUME_REFUSED` (reason
  in the body) for a state it would have to guess at: another active version, a file
  at both or neither place, a symlink, a file another row names, a size mismatch,
  more than one swapping version of a kind. 409 `NO_SWAP_TO_RESUME` when the book has
  no unfinished swap; a failed resume is 500 `VERSION_SWAP_INCOMPLETE` and repeats
  safely. It also repairs the rarer leftover where only the last status write failed
  (leaving version `alt`, entering version `swapping_in`).
- Trashing any version of a book that has a `swapping_*` version is refused with
  409 `VERSION_SWAPPING` until the swap is resumed (a retried DELETE of the version
  that already reads `alt` is no longer trashed).
- The nightly `maintenance.trash-cleanup` lists these books (`swap_incomplete`,
  `swap_incomplete_version_ids`, `swap_incomplete_book_ids`,
  `swap_incomplete_file_paths`, `swap_incomplete_books`) with one WARN log line and
  a link to the new tab. It never resumes a swap, and fails the night only when it
  could not list them. The listing is a status-taking scan
  (`database.BookVersionDispositionReader.ListSwappingBookVersions`; there is no
  status index, like `ListTrashedBookVersions`).
- The never-called `versions.ResumeVersionSwaps` stub is removed.
