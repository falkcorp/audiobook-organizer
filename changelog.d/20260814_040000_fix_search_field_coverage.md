### Fixed

- Thirteen fields the library search bar offers returned "no books found" for
  every query. `year`, `series_number`, `isbn10`, `isbn13`, `work_id`,
  `channels`, `bit_depth`, `created_at`, `updated_at`, `duration`, `file_size`,
  `bitrate` and `sample_rate` all parsed correctly in the UI, travelled to the
  server as well-formed filters, and fell through the backend's unknown-field
  branch, which matches nothing. The last four are the same columns the backend
  did implement, under unit-suffixed names the search bar never sends —
  measured on production, `duration:1` answered 0 while `duration_seconds:1`
  answered 25,090 over the same rows. Both spellings are now accepted.

- `marked_for_deletion` works as a filter. It previously answered `count: 0`
  against a library holding 3,953 soft-deleted books, for two stacked reasons:
  the field was not implemented at all, and the rows are excluded before any
  filter runs, so asking for them inside an already-live-only set could only
  ever return zero. The filter now sets the store's tri-state so the rows reach
  it. `year` matches either the print year or the audiobook release year, since
  a book carries both and they routinely differ.

- A filter naming a field the list cannot filter on is now rejected with a 400
  that names the field and lists the valid ones, instead of being answered with
  `count: 0` — an answer indistinguishable from a truthful "no books match".
