### Changed

- **Trash purge keeps listening progress safe without keeping books forever
  (owner decision 2026-10-05).** Since #3764 the nightly purge skipped every
  trashed book a user still had listening state on, with nothing to ever
  clear it.
  - When a version of the book that Audiobookshelf lists is in its version
    group, the purge now carries every user's state there first, all or
    nothing, through `merge.CarryStateThenHardDelete` (its precheck
    re-checks the listing under the merge lock), and purges the book in the
    same hold of the lock. A hidden copy (not primary, not organized,
    quarantined) never receives the state. A carry that does not complete
    keeps the book with the state put back on it.
  - With no listed version the book stays in the trash. `GET
    /audiobooks/soft-deleted` rows now carry `has_progress`,
    `progress_summary` (for example "reader: 42%, at 1:02:03") and
    `progress_unknown`.
  - New `POST /audiobooks/:id/discard-progress-and-purge` (library delete
    permission): clears every user's book state, positions and bookmarks on
    one trashed book and purges it through the purge's own delete path. It
    refuses a book that is not in the trash (409), one that still owns
    `book_file` rows (409, checked before anything is cleared), one a
    pending user-state repair names (409), and any call with no activity
    log wired (503). Each discard writes an audit-tier activity row with
    the actor.
  - The purge result replaces `skipped_has_user_state` with
    `carried_to_sibling`, `kept_has_progress` and `carry_failed` (each with
    an `_ids` list), and the nightly result line reports all three.
  - The purge now runs a bounded worker pool (one version group per worker
    at a time) instead of one book at a time.
  - Web: the trash list shows a "has progress" tag and a "Discard progress
    and purge" button with a confirm dialog naming the book and the progress
    that will be lost.
