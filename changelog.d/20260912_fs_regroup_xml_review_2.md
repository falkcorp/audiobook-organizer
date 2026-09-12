### Fixed

#### fs-regroup-xml: apply re-checks its plan, and title reverts keep later edits

Second round of review fixes for the chapter-per-folder repair (#3326, #3331).
None of it has run against the library yet.

- Undoing a metadata change now restores the old value only while the field
  still holds what the operation wrote. A title (or any other field) edited
  since is left as it is, the undo preview lists it as changed since, and the
  revert result counts it separately.
- The apply now re-checks, under the merge lock, the conditions the dry run
  refused on. It skips a group when another book has taken the book folder, a
  member is no longer the primary version or the members now span version
  groups, or a member has rows other than the plan saw.
- A member file outside a "Title - N" chapter folder is refused, in the dry run
  and at apply time, instead of keeping a track number that can collide.
- fs-regroup-xml, missing-file-repoint, recover-missing-files,
  merge-same-path-dupes and dedupe-book-file-rows no longer run at the same
  time. Each one waits for the others to finish.
- Moving an external id back during an undo now holds the merge lock.
