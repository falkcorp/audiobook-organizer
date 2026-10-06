### Fixed

- **Follow-ups to the merge-state carry (#3777 review).**
  - The trash purge deletes a file from disk only when the path is inside
    the library root (`root_dir`). A path outside the root (a sibling
    directory, a `..` or symlink escape, the root itself, or any path when
    no root is configured) is kept, and the purge reports it as
    `book purged, file kept`. The code comment had promised this check, but
    nothing enforced it.
  - Undoing a merge no longer rewinds a newer position. The survivor
    reconcile wrote positions before it took the per-(user, book) lock and
    checked for newer writes. It now takes the lock, re-reads the state and
    the positions, and redoes the reconcile if either changed. The
    absorbed-side restore now reads and writes under the same lock.
  - Reverting an Audible read-status import restores positions in one
    atomic replace (`ReplaceUserPositions`). Before, it cleared them and
    then rewrote each one, so a failure partway through left no position.
  - A book whose bookmark check fails for a permanent reason now says which
    reason in the purge report: a broken sync redirect chain
    (`merge.ErrBookmarkCheckRedirectBroken`) or an alias graph over the cap
    (`merge.ErrBookmarkCheckAliasLimit`). Before, every run reported the
    generic "cannot read users' listening state".
  - `DELETE /audiobooks/:id` and discard-progress-and-purge answer a
    file-rows refusal with 409 code `OWNS_FILES`, and the web keys on that
    code. A live hard delete whose state carry does not complete answers
    500 `CARRY_FAILED`, as Purge now already did. Before, that case
    answered `DELETE_FAILED`.
  - `readstatus.RecomputeUserBookState` stamps a record that has positions
    but no timestamp anywhere (legacy rows) with the recompute time.
