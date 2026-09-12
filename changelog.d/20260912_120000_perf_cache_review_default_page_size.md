### Changed

#### `GET /audiobooks/metadata/cache/review` caps an unpaged request to 200 rows; `all=true` returns everything

A request with no positive `limit` used to return every reviewable row in one
response, so a caller that forgot to page (or a stray curl) paid for the whole
cache. It is now capped to a default page of 200 rows unless it sends
`all=true`. The cap is not silent: the response carries `truncated` (the rows
returned are not the whole set, same meaning as on `GET /operations/timeline`)
and the `limit` actually applied (0 when `all=true`), `total_count` still
reports the whole set, and the server logs a WARN when the default cap
truncates a response. An explicit positive `limit` is honoured exactly as
before. The only caller that needs every row, the metadata review lane
(`useMetadataLane`), now sends `all=true`, so its client-side filtering,
grouping and stale-set derivation are unchanged; if a response still comes
back `truncated`, the lane shows a warning instead of presenting the partial
set as the whole library. The handler also logs a WARN
when it takes longer than 5s, with the row counts and whether a `library.scan`
is queued or running, so a slow request can be correlated instead of only
reported. This does not make the endpoint faster for the review lane: the
per-row cached-candidate read still runs over every prepared row whatever the
page size.
