### Fixed

#### fs-regroup-xml: safer merges and a revert that never overwrites later edits

Review fixes for the chapter-per-folder repair added in #3326. None of it has
run against the library yet.

- A group is refused, in the dry run and again at apply time, when two books
  stand for the same file, when a book's rows miss its own path, when two paths
  claim one chapter number, when a book with no row has no file on disk, when
  the books span version groups or one is not the primary version, or when an
  earlier merge's survivor already sits in the book folder.
- A missing row is created on the book whose path it is and then moved to the
  survivor, so undoing the merge leaves every row on the book it belongs to.
  Track numbers come from each file's own chapter folder.
- The apply re-reads every book under the merge lock and skips a group that
  changed since the plan, including one whose survivor was deleted in the
  meantime, and it re-checks protected paths on what it re-reads.
- Retired books are marked non-primary, and their external ids move one at a
  time. Both are recorded so a revert can undo them.
- A revert restores a track number, path, primary flag or external id only if
  it still holds the value the repair wrote. Anything edited since is refused
  and shown in the undo preview.
