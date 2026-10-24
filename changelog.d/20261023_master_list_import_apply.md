### Added

#### `master-list-import` — apply, read-elsewhere records, list retention

PR 2 of the master-list import. The fixer now applies. Apply is a dry run by default and requires `users.manage`; it writes through the Repairs writer, so every change is journaled and undoable, and the framework guards, the scan stand-down and the changed-since-plan re-check all apply. Planning is two passes: the match of PR 1, then a per-row decision that mirrors the Audible import's rules. For a matched row the apply proposes only `user`, `read_status`, `finished_at`, `progress_pct` and `position_sec`:

- `finished` rows (with a finish time) mark the book finished for the target user, manual, at the list's time; a book with no position gets one at its end.
- `in_progress` rows (with a last-listened time and a position or percent) set the position at the list's time.
- Never applied, and listed under their own class instead: a row with no status (`skipped_no_status`), `unstarted` or `abandoned` in the list, no usable timestamp or progress, a book already finished locally, newer local activity on the book or a copy of it (`skipped_newer_local`), local progress an in-progress row would overwrite, and a book the user abandoned or set to unstarted by hand. Each class is a clickable filter of the rows.

A row that matches no library book, with no title candidates and no trashed copy carrying its identifier, becomes a `read_elsewhere` row. Applying it stores a record under the new `mlist:elsewhere:` key family (registered in the key-family census), never a book: `GET /api/v1/master-lists/elsewhere` lists the records (admin, `users.manage`, optional `user_id`), and the plan summary counts them. Creating a record is journaled with no book and undone by deleting it; a record is created only if absent, so a second import of the same row (from this list or another) is `skipped_already_recorded`, and a record that appears between plan and apply is kept and reported `changed_since_plan`.

`DELETE /api/v1/master-lists/:id` removes a stored list and its rows (404 for an unknown id; read-elsewhere records made from it stay). Uploads now keep the uploader's newest `master_list_retention` lists (default 10; 0 or a negative value means the default) and prune the rest after the new list is stored; the upload response names the pruned ids and any retention error, and a retention failure never fails the upload. Retention never touches read-elsewhere records.

### Fixed

- An ISBN-10 whose check digit is `X` is read as an ISBN-10 instead of an ASIN.
- A generic `isbn` column that disagrees with an `isbn10` or `isbn13` column on the same row is an `identifier_conflict` finding instead of being dropped silently.
- A JSON list with a wrongly typed field in one row keeps the file: the row carries a `bad_field` finding (or `bad_row` when the row itself is not an object).
- A row whose ASIN names several editions is settled by its ISBN-13 when that names one of them, and held for review as `identifier_conflict` when the ISBN names a book outside them.
- The ASIN and ISBN agreement check follows a trashed copy to its live survivor, so an ISBN carried only by a book merged away is no longer invisible to it.
