### Removed

#### Review > Metadata no longer has a full-index mode, and `review_metadata_server_query` is gone

The Metadata lane always asks the server for one filtered page (`view=page`) plus the chip
counts, and fetches full rows for the rows on screen with `ids=`. The browser-side path that
loaded every reviewable row and filtered, counted, grouped and paged them in memory is
deleted, along with the setting that chose between the two ways, so a Review tab can no
longer hold the whole library. Select all N matching, the title filter, the provider and
confidence filters, the runtime, language, transcription and multi-book switches and every
chip view are answered by the server, as they have been while the setting was on.

`GET /api/v1/audiobooks/metadata/cache/review?view=index` now answers `410 Gone` with a JSON
body (`code` `VIEW_INDEX_GONE`, `use` `view=page`) and is recorded in
`review_index_request_seconds` under `view="gone"`, so a client that still sends it shows up
in the metric. `view=page`, `ids=` and the default listing are unchanged.

The setting `review_metadata_server_query` is removed. A stored config or settings row that
still contains it loads without error; startup logs that it is ignored, a config-file save
drops it, and a `PUT /config` carrying it is refused with the removal message like the other
retired settings.

The precondition for this change (the setting on for a full release) was waived by the owner
on 2026-10-19 ("Go ahead now"). The setting has been on in production since 2026-10-18 16:05
with no review errors, 02-PR18 (`941d9521c`) shipped in v0.222.2, and no full release has gone
out since the setting was turned on.

### Changed

#### Review latency metric: the `index` view label is replaced by `gone`

`review_index_request_seconds{view}` now carries `full` and `gone`. `view="index"` is no
longer written. The overnight dashboard's "Review index p95" panel still queries
`view="index"` and will read no data; `view=page` requests are not observed by this metric.
