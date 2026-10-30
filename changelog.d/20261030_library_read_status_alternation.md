### Fixed

- The Library list's `filters=` route now reads a read_status value written as a
  bare alternation (`finished|in_progress`) as the union of those statuses, the
  same as `(finished|in_progress)` and the search box. An unknown status in the
  alternation is a 400 that names it and lists the valid statuses.
