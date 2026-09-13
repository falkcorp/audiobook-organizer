### Fixed

#### fs-regroup-xml: apply re-checks its plan, and title reverts keep later edits

Second round of review fixes for the chapter-per-folder repair (#3326, #3331).
None of it has run against the library yet.

- Undoing a metadata change now restores the old value only while the field
  still holds what the operation wrote. A title (or any other field) edited
  since is left as it is, the undo preview lists it as changed since, and the
  revert result counts it separately. A field that already holds the old value
  (a rescan that put a book back to "imported", say) counts as restored, so a
  retried undo no longer reports it as partial every time. This is the same
  check series-rename undo uses, and a series renamed again since the
  operation is counted in the same "changed since" total.
- The apply now re-checks, under the merge lock, the conditions the dry run
  refused on. It skips a group when another live book sits in the book folder
  (every such book is checked, not only the one the path index names), a
  member is no longer the primary version or the members now span version
  groups, or a member has rows other than the plan saw.
- A member file outside a "Title - N" chapter folder is refused, in the dry run
  and at apply time, instead of keeping a track number that can collide.
- fs-regroup-xml, missing-file-repoint, recover-missing-files,
  merge-same-path-dupes and dedupe-book-file-rows no longer run at the same
  time. Each one waits for the others to finish.
- Moving an external id back during an undo now holds the merge lock.
