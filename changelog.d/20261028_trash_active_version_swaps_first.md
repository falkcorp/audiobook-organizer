### Fixed

#### Trashing a book's active version switches the book to another version first

Moving the version a book is playing to the trash used to flip its status and
nothing else. The book kept playing that version's files, and 14 days later the
trash cleanup deleted them. `DELETE /api/v1/audiobooks/:id/versions/:vid` on the
active version now makes another version active first (`versions.RunVersionSwap`:
the new version's files move up to the book folder, the old version's into its
`.versions/{id}/` slot, and the book's file path follows), and only then trashes
the version that left. The version that takes over is the most recently ingested
`alt` version whose files the swap can safely take (`versions.PickSwapTarget`,
see below); a trashed, purging, purged or retired version is never chosen. With no such version
the request is refused with a 409 and code `NO_SWAP_TARGET` that lists why each
other version was passed over, and nothing is written; if the swap itself fails
the version is not trashed (500, code `VERSION_SWAP_FAILED`). The response carries
`swapped_to`, the version that became active. The whole step, for every version
trashed, now runs under the merge lock (a running merge answers 409 "try again"),
so two version operations on one book cannot interleave.

A multi-file book whose path is a folder is refused the same way (409
`NO_SWAP_TARGET`), as is a direct `RunVersionSwap` on it: the swap takes the
book's folder to be the parent of its path, which for a folder would move the new
version's files in among other books'.

`RunVersionSwap` now refuses a retired version on either side before it writes
anything (the leftover tracked as TODO-RETIRED-SWAP-GUARD), and a swap that fails
before moving any file puts both versions back to the statuses they had instead
of leaving the book with no active version. `versions.AutoPromoteAlt`, which
promoted an alt without moving a file, is removed.

The swap only takes a version whose files are in its own `.versions/{id}/` slot,
and only when the book's own path is the folder those slots belong to. Before it
writes anything (in the picker, and again at the start of `RunVersionSwap`) it
refuses, naming the reason in the 409 body: a book path that is empty, relative or
stale (the active version's files are not in the folder the path names); an
entering file outside its slot, in the form stored or with symlinks resolved (a
download folder, another book's folder, a slot that is a symlink); an entering
file that is a symbolic link; and an entering file that is the same file as one of
the leaving version's (the same path, the same path through a link, or the same
inode). Two of these were reproduced as data loss or a stuck book: an alt whose
row names the active version's file (the swap renamed the only copy into the
leaving slot, trashed the version, and the purge deleted it), and an alt that is
a symlink to the active file (the swap failed on its third step and left the book
`swapping_out`/`swapping_in` with no active version, so every later trash was a
409). Nothing in production places alt files in their slot today, so most existing
alt versions will answer 409 `NO_SWAP_TARGET` until a follow-up lets the swap adopt
files from elsewhere safely (todo.d).

A swap that fails once it has started now puts everything back: the statuses
(and with them the book's active version) after any failure up to the point the
files have moved for good, and the files themselves when the third step fails
(the leaving file is moved back, any entering file that moved goes back to its
slot, the empty slot folder is removed). After the files have moved,
a failed database write (a file row, the book's `file_path`, the final statuses)
is returned as an error and the version is not trashed, instead of being logged
and ignored; the rows are written with `ModifyBookFile`, which refuses a row that
no longer names the path the file was moved from. A refusal before the first
write is a 409 `NO_SWAP_TARGET`, not a 500 `VERSION_SWAP_FAILED`, and a trash
that fails after the swap went through answers with `swapped_to`.

A file that is missing is refused, not skipped. The first form of the swap treated
"source missing, destination present" as a move that had already happened, so an
alt whose own file was gone but whose name matched a stray file in the book folder
(or in the leaving slot) was "adopted" and the stray file was treated as its audio.
The move now requires its source to exist and its destination to be free: the
picker and the preflight refuse with "file X is missing, so there is nothing to
move" or "something is already there", and the rename itself refuses to replace
anything (`renameat2` `RENAME_NOREPLACE` on Linux, `renamex_np` `RENAME_EXCL` on
macOS, a checked rename elsewhere). The preflight also refuses a `.versions`
folder or a version slot that is a symbolic link or a file, or that resolves
outside the book's folder, and a book path with a trailing separator ("not a
file"). Undoing a failed swap puts back exactly the files this call renamed, in
reverse order, and reports an error (leaving the swap `ErrSwapIncomplete`) when an
original path has been taken in the meantime instead of overwriting it.

A swap that did not finish can no longer be made worse by retrying. A version in
`swapping_out` / `swapping_in` cannot be trashed (`MarkTrashed` refuses it; 409
`VERSION_SWAPPING`), nor swapped again (`ErrSwapRefused`: resuming a swap is not
implemented). An incomplete swap answers 500 `VERSION_SWAP_INCOMPLETE`, which says
the versions are stuck mid-swap and need repair and that deleting again will not
help; it no longer reads as a failure that changed nothing. Finding and repairing
those books is tracked in `todo.d` (TODO-SWAP-STUCK-RECOVERY).

The trash purge refuses to delete a trashed version's files when none of the files
the book serves (its `file_path`, the rows of its active version) exists on disk,
and a `file_path` that is a folder counts only if it holds a regular file outside
`.versions/`: that state means the guard cannot tell what is served, not that
nothing is. The version stays in the trash (409 from Purge now). This is its own
case, `NothingServedError` (`errors.Is` `ErrPurgeNothingServed`), not "cannot
tell": the nightly cleanup lists it under `NothingServed` with the book's path,
the operation result has `nothing_served` and `nothing_served_version_ids`,
`nothing_served_book_ids` and `nothing_served_file_paths`, and the Trash cleanup
panel says "the book serves nothing that exists on disk" and shows the book path
to fix, instead of "the files are missing / a drive is down". The version is
never claimed, so there is no attempt count to escalate; it stays listed every
night until the book's path is fixed or the version restored.
