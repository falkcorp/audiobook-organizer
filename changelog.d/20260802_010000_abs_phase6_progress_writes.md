<!-- file: changelog.d/20260802_010000_abs_phase6_progress_writes.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6c2f81ad-4b39-4e70-95c1-8a03df2b71e6 -->
<!-- last-edited: 2026-08-02 -->

### Added

- **Audiobookshelf Phase 6 write half — progress mutations and bookmarks.** The read
  half shipped earlier, so every client-side progress change 404'd: "reset progress"
  and "remove from continue listening" appeared to do nothing. Adds
  `GET`/`PATCH`/`DELETE /api/me/progress/:id`, `GET /api/me/progress`,
  `PATCH /api/me/progress/batch/update`,
  `POST /api/me/item/:id/remove-from-continue-listening`, and bookmarks CRUD
  (`GET /api/me/bookmarks/:id`, `POST`/`PATCH /api/me/item/:id/bookmark`,
  `DELETE /api/me/item/:id/bookmark/:time`) over the existing progress and bookmark
  keyspaces. Writes merge through the §5 conflict policy (`progress.MergeExplicit`)
  rather than last-write-wins.

- **`hideFromContinueListening` is now persisted** (`UserBookState`), so removing a
  book from Continue Listening survives a restart and is reported consistently by
  `/api/me`, `/api/me/progress` and `/api/items/:id`.

### Fixed

- **A playback sync silently reset user intent on the book state.**
  `persistProgress` constructed a fresh `UserBookState` on every sync instead of
  read-modify-writing it, so every field it did not set was reset. That already
  un-pinned a hand-set read status (`StatusManual`) roughly every 20 seconds of
  listening, and would have un-hidden any book removed from Continue Listening within
  seconds of the user hiding it. All ABS write paths now go through a single
  read-modify-write helper.

### Changed

- **Two spec assumptions corrected against the reference oracle** (real ABS 2.36.0,
  probed 2026-08-02; five new fixtures committed under `testdata/abs-fixtures/`):
  - `DELETE /api/me/progress/:id` is keyed by the **`mediaProgress` row id**, not the
    `libraryItemId` — deleting by item id answers 404 on real ABS. Our handlers accept
    **both** forms, since a client may hold either.
  - `POST /api/me/item/:id/remove-from-continue-listening` **does not exist** on ABS
    2.36.0 (it answers "Cannot POST"); the real mechanism is a
    `hideFromContinueListening` field on `PATCH /api/me/progress/:id`. Both are served,
    because a client calls the POST form and was taking a 404.
