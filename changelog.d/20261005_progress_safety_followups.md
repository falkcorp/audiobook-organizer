### Fixed

- **Progress-safety follow-ups to #3771 and #3772 (owner decisions
  2026-10-05).**
  - "Purge now" on the trash page (`DELETE /audiobooks/:id` for a trashed
    book) now runs the nightly purge's own path: listening state moves to
    the copy Audiobookshelf lists and then the book is purged; with no such
    copy it is refused with 409 and code `HAS_PROGRESS`, and the page opens
    "Discard progress and purge". A hard delete of a live book and the batch
    hard delete carry-or-refuse the same way
    (`merge.HardDeleteKeepingUserState`). Other delete failures now answer
    500 with the reason instead of 404. "Purge now" honours
    `purge_soft_deleted_delete_files` like the nightly purge.
  - Bookmarks count as listening state: a book whose only state is a
    bookmark is no longer hard-deleted by the purge, reconcile, the iTunes
    regroup or a user delete, and discard counts bookmark-only users.
  - Combining two copies keeps the larger listened time (never summed, never
    just the newer copy's).
  - The undo and restore paths replace a user's positions in one atomic
    write (`database.UserPositionReplacer`), undated positions decide only
    positions (per segment) while the status follows time, the
    touched-survivor reconcile re-reads right before its write and drops its
    marker when the write fails, and its markers are cleaned once their
    journal is done. `SetUserPositionAt` keeps a zero (undated) timestamp.
  - The iTunes regroup fails closed when it cannot read whether a book is
    listed.
  - Trash: the discard re-checks file rows under the merge lock; a sibling
    read error is `carry_failed`; the listing adds `listed_copy_id`,
    `listed_copy_unknown`, `purge_eligible` and `progress_other_users`, and
    shows other users' names only to a user who may manage users; the
    purge's external-ID tombstones and iTunes removes run after the row is
    deleted, and an iTunes track another copy still holds is not removed.
