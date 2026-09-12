### Added

#### `/audiobooks` list response echoes the filters the server actually applied

The `ListAudiobooks` (`GET /audiobooks`) response now includes an
`applied_filters` array of `{field, value}` pairs covering the advanced
`filters=` JSON param (both book-global and per-user fields) plus the simple
query params the request used: `library_state`, `tag`, `tags`,
`fingerprint_status`, `coverage_percent_min`/`coverage_percent_max`, and
`sort_by`/`sort_order`. The key is always present, even as an empty array
when no filters were sent, so the frontend can render active-filter chips
from ground truth instead of guessing from what it sent. Purely additive: no
existing response field changed and no change to which books are returned.
